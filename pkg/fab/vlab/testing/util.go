package testing

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pkg/errors"
	agentapi "go.githedgehog.com/fabric/api/agent/v1alpha2"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func WaitForSwitchesReady(ctx context.Context, kube client.WithWatch, expectedSwitches []string) error {
	start := time.Now()

	ready := map[string]bool{}
	for _, switchName := range expectedSwitches {
		ready[switchName] = false
	}

	errs := 0
	retries := 8

	for {
		time.Sleep(15 * time.Second) // TODO make configurable

		agents := agentapi.AgentList{}
		if err := kube.List(ctx, &agents, client.InNamespace("default")); err != nil {
			errs++
			if errs <= retries {
				slog.Warn("Error listing agents", "retries", fmt.Sprintf("%d/%d", errs, retries), "err", err)

				continue
			}

			return errors.Wrapf(err, "error listing agents")
		}

		for _, agent := range agents.Items {
			if agent.Generation == agent.Status.LastAppliedGen && time.Since(agent.Status.LastHeartbeat.Time) < 30*time.Second {
				ready[agent.Name] = true

				continue
			}
		}

		allReady := true
		for _, swReady := range ready {
			if !swReady {
				allReady = false
			}
		}

		readyList := []string{}
		notReadyList := []string{}
		for sw, swReady := range ready {
			if swReady {
				readyList = append(readyList, sw)
			} else {
				notReadyList = append(notReadyList, sw)
			}
		}

		slog.Info("Switches ready status", "ready", readyList, "notReady", notReadyList)

		if allReady {
			slog.Info("All switches are ready", "took", time.Since(start))

			return nil
		}
	}
}

func BuildNetconf(ctx context.Context, kube client.Client, server string) ([]string, error) {
	attached, err := apiutil.GetAttachedSubnets(ctx, kube, server)
	if err != nil {
		return nil, errors.Wrapf(err, "error getting attached subnets")
	}

	netconfs := []string{}
	for vpcSubnet, attachment := range attached {
		vpcParts := strings.SplitN(vpcSubnet, "/", 2)
		if len(vpcParts) != 2 {
			return nil, errors.Errorf("invalid subnet format: %s", vpcSubnet)
		}

		vpcName, subnetName := vpcParts[0], vpcParts[1]

		vpc := vpcapi.VPC{}
		if err := kube.Get(ctx, client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      vpcName,
		}, &vpc); err != nil {
			return nil, errors.Wrapf(err, "failed to get VPC %s", vpcName)
		}

		if vpc.Spec.Subnets[subnetName] == nil {
			return nil, errors.Errorf("subnet %s not found in VPC %s", subnetName, vpcName)
		}

		conn := wiringapi.Connection{}
		if err := kube.Get(ctx, client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      attachment.Connection,
		}, &conn); err != nil {
			return nil, errors.Wrapf(err, "failed to get connection %s", attachment.Connection)
		}

		vlan := "0"
		if !attachment.NativeVLAN {
			vlan = vpc.Spec.Subnets[subnetName].VLAN
		}

		netconf := ""
		if conn.Spec.Unbundled != nil {
			netconf = "vlan " + vlan + " " + conn.Spec.Unbundled.Link.Server.LocalPortName()
		} else {
			netconf = "bond " + vlan

			if conn.Spec.Bundled != nil {
				for _, link := range conn.Spec.Bundled.Links {
					netconf += " " + link.Server.LocalPortName()
				}
			}
			if conn.Spec.MCLAG != nil {
				for _, link := range conn.Spec.MCLAG.Links {
					netconf += " " + link.Server.LocalPortName()
				}
			}
			if conn.Spec.ESLAG != nil {
				for _, link := range conn.Spec.ESLAG.Links {
					netconf += " " + link.Server.LocalPortName()
				}
			}
		}

		netconfs = append(netconfs, netconf)
	}

	return netconfs, nil
}
