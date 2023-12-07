package vlab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/melbahja/goph"
	"github.com/pkg/errors"
	agentapi "go.githedgehog.com/fabric/api/agent/v1alpha2"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(vpcapi.AddToScheme(scheme))
	utilruntime.Must(wiringapi.AddToScheme(scheme))
	utilruntime.Must(agentapi.AddToScheme(scheme))
}

func kubeClient() (client.WithWatch, error) {
	k8scfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	client, err := client.NewWithWatch(k8scfg, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, err
	}

	return client, nil
}

type netConfig struct {
	Name    string
	SSHPort uint
	Net     string
}

func (svc *Service) CreateVPCPerServer() error {
	os.Setenv("KUBECONFIG", filepath.Join(svc.cfg.Basedir, "kubeconfig.yaml"))
	kube, err := kubeClient()
	if err != nil {
		return errors.Wrapf(err, "error creating kube client")
	}

	idx := 1

	netconfs := []netConfig{}
	for _, server := range svc.cfg.Wiring.Server.All() {
		if server.IsControl() {
			continue
		}

		vm := svc.mngr.vms[server.Name]
		if vm == nil {
			return errors.Errorf("no VM found for server %s", server.Name)
		}

		var conn *wiringapi.Connection
		for _, some := range svc.cfg.Wiring.Connection.All() {
			_, servers, _, _, err := some.Spec.Endpoints()
			if err != nil {
				return errors.Wrapf(err, "error getting endpoints for connection %s", some.Name)
			}

			if !slices.Contains(servers, server.Name) {
				continue
			}

			if some.Spec.Unbundled == nil && some.Spec.Bundled == nil && some.Spec.MCLAG == nil {
				continue
			}

			conn = some
		}

		if conn == nil {
			slog.Info("Skipping server (no connection)...", "server", server.Name)
			return nil
		}

		vpcName, _ := strings.CutPrefix(server.Name, "server-")
		vpcName = "vpc-" + vpcName

		slog.Info("Creating VPC + Attachment for server...", "vpc", vpcName, "server", server.Name, "conn", conn.Name)

		vlan := fmt.Sprintf("%d", 1000+idx)
		vpc := &vpcapi.VPC{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("vpc-%d", idx),
				Namespace: "default", // TODO ns
			},
		}
		_, err = ctrlutil.CreateOrUpdate(context.TODO(), kube, vpc, func() error {
			vpc.Spec = vpcapi.VPCSpec{
				Subnets: map[string]*vpcapi.VPCSubnet{
					"default": {
						Subnet: fmt.Sprintf("10.0.%d.0/24", idx),
						VLAN:   vlan,
						DHCP: vpcapi.VPCDHCP{
							Enable: true,
							Range: &vpcapi.VPCDHCPRange{
								Start: fmt.Sprintf("10.0.%d.10", idx),
							},
						},
					},
				},
			}

			return nil
		})
		if err != nil {
			return errors.Wrapf(err, "error creating/updating VPC %s", vpc.Name)
		}

		attach := &vpcapi.VPCAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", vpcName, conn.Name),
				Namespace: "default", // TODO ns
			},
		}
		_, err = ctrlutil.CreateOrUpdate(context.TODO(), kube, attach, func() error {
			attach.Spec = vpcapi.VPCAttachmentSpec{
				Subnet:     vpc.Name + "/default",
				Connection: conn.Name,
			}

			return nil
		})
		if err != nil {
			return errors.Wrapf(err, "error creating/updating VPC attachment %s", attach.Name)
		}

		net := ""
		if conn.Spec.Unbundled != nil {
			net = "vlan " + vlan + " " + conn.Spec.Unbundled.Link.Server.LocalPortName()
		} else {
			net = "bond " + vlan

			if conn.Spec.Bundled != nil {
				for _, link := range conn.Spec.Bundled.Links {
					net += " " + link.Server.LocalPortName()
				}
			}
			if conn.Spec.MCLAG != nil {
				for _, link := range conn.Spec.MCLAG.Links {
					net += " " + link.Server.LocalPortName()
				}
			}
		}

		netconfs = append(netconfs, netConfig{
			Name:    server.Name,
			SSHPort: uint(vm.sshPort()),
			Net:     net,
		})

		idx += 1
	}

	auth, err := goph.Key(svc.cfg.SshKey, "")
	if err != nil {
		return errors.Wrapf(err, "error loading SSH key")
	}

	for _, netconf := range netconfs {
		slog.Info("Configuring networking for server...", "server", netconf.Name, "netconf", netconf.Net)

		client, err := goph.NewConn(&goph.Config{
			User:     "core",
			Addr:     "127.0.0.1",
			Port:     netconf.SSHPort,
			Auth:     auth,
			Timeout:  30 * time.Second,
			Callback: ssh.InsecureIgnoreHostKey(),
		})
		if err != nil {
			return errors.Wrapf(err, "error creating SSH client")
		}

		out, err := client.Run("/opt/bin/hhnet cleanup")
		if err != nil {
			slog.Warn("hhnet cleanup error", "err", err, "output", string(out))
			return errors.Wrapf(err, "error running hhnet cleanup")
		}

		out, err = client.Run("/opt/bin/hhnet " + netconf.Net)
		if err != nil {
			slog.Warn("hhnet conf error", "err", err, "output", string(out))
			return errors.Wrapf(err, "error running hhnet")
		}

		strOut := strings.TrimSpace(string(out))

		slog.Info("Server network configured", "server", netconf.Name, "output", strOut)
	}

	return nil
}
