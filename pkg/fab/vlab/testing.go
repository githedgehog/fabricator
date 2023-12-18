package vlab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
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

func (svc *Service) SetupVPCPerServer(ctx context.Context) error {
	start := time.Now()

	slog.Info("Setting up VPCs and VPCAttachments per server")

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

		slog.Info("Enforcing VPC + Attachment for server...", "vpc", vpcName, "server", server.Name, "conn", conn.Name)

		vlan := fmt.Sprintf("%d", 1000+idx)
		vpc := &vpcapi.VPC{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("vpc-%d", idx),
				Namespace: "default", // TODO ns
			},
		}
		_, err = ctrlutil.CreateOrUpdate(ctx, kube, vpc, func() error {
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
		_, err = ctrlutil.CreateOrUpdate(ctx, kube, attach, func() error {
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
		start := time.Now()

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

		slog.Info("Server network configured", "server", netconf.Name, "output", strOut, "took", time.Since(start))
	}

	slog.Info("VPCs and VPCAttachments created, IP addresses discovered", "took", time.Since(start))

	return nil
}

func checkAgents(ctx context.Context, kube client.WithWatch) error {
	agentList := &agentapi.AgentList{}
	if err := kube.List(ctx, agentList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing agents")
	}

	for _, agent := range agentList.Items {
		if agent.Status.LastHeartbeat.Time.Before(time.Now().Add(-2 * time.Minute)) {
			return errors.Errorf("agent %s last heartbeat is too old", agent.Name)
		}

		if agent.Status.LastAppliedGen != agent.Generation {
			return errors.Errorf("agent %s last applied gen %d doesn't match current gen %d", agent.Name, agent.Status.LastAppliedGen, agent.Generation)
		}
	}

	return nil
}

type ServerConnectivityTestConfig struct {
	AgentCheck bool

	VPC      bool
	VPCPing  uint
	VPCIperf uint

	Ext     bool
	ExtCurl bool
}

func (svc *Service) TestServerConnectivity(ctx context.Context, cfg ServerConnectivityTestConfig) error {
	start := time.Now()

	slog.Info("Starting connectivity test", "vpc", cfg.VPC, "vpcPing", cfg.VPCPing, "vpcIperf", cfg.VPCIperf, "ext", cfg.Ext, "extCurl", cfg.ExtCurl)

	os.Setenv("KUBECONFIG", filepath.Join(svc.cfg.Basedir, "kubeconfig.yaml"))
	kube, err := kubeClient()
	if err != nil {
		return errors.Wrapf(err, "error creating kube client")
	}

	if cfg.AgentCheck {
		if err := checkAgents(ctx, kube); err != nil {
			return errors.Wrapf(err, "error checking agents")
		}
	}

	vpcAttachList := &vpcapi.VPCAttachmentList{}
	if err := kube.List(ctx, vpcAttachList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing VPC attachments")
	}

	vpcPeeringList := &vpcapi.VPCPeeringList{}
	if err := kube.List(ctx, vpcPeeringList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing VPC peerings")
	}

	vpcList := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing VPCs")
	}

	externalPeeringList := &vpcapi.ExternalPeeringList{}
	if err := kube.List(ctx, externalPeeringList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing external peerings")
	}

	servers := map[string]*Server{}

serverLoop:
	for _, server := range svc.cfg.Wiring.Server.All() {
		if server.IsControl() {
			continue
		}

		slog.Debug("Processing", "server", server.Name)

		vm := svc.mngr.vms[server.Name]
		if vm == nil {
			slog.Info("Skipping server (no VM)...", "server", server.Name)
			continue
		}

		srv := &Server{
			Name:     server.Name,
			Server:   server,
			VM:       vm,
			VPCPeers: []string{},
		}

		for _, some := range svc.cfg.Wiring.Connection.All() {
			if some.Spec.Unbundled == nil && some.Spec.Bundled == nil && some.Spec.MCLAG == nil {
				continue
			}

			switches, servers, _, _, err := some.Spec.Endpoints()
			if err != nil {
				return errors.Wrapf(err, "error getting endpoints for connection %s", some.Name)
			}

			if len(servers) != 1 {
				slog.Info("Skipping server (multiple servers in connection)...", "server", server.Name)
				continue serverLoop
			}
			if !slices.Contains(servers, server.Name) {
				continue
			}

			if srv.Connection != nil {
				slog.Info("Skipping server (multiple connections)...", "server", server.Name)
				continue serverLoop
			}

			srv.ConnectedTo = switches
			srv.Connection = some

			if some.Spec.Unbundled != nil {
				srv.ConnectionType = wiringapi.CONNECTION_TYPE_UNBUNDLED
			} else if some.Spec.Bundled != nil {
				srv.ConnectionType = wiringapi.CONNECTION_TYPE_BUNDLED
			} else if some.Spec.MCLAG != nil {
				srv.ConnectionType = wiringapi.CONNECTION_TYPE_MCLAG
			}
		}

		if srv.Connection == nil {
			slog.Info("Skipping server (no connection)...", "server", server.Name)
			continue
		}

		for _, some := range vpcAttachList.Items {
			if some.Spec.Connection != srv.Connection.Name {
				continue
			}

			if srv.VPCAttachment != nil {
				slog.Info("Skipping server (multiple VPC attachments)...", "server", server.Name)
				continue
			}

			someCopy := some
			srv.VPCAttachment = &someCopy
			srv.Subnet = some.Spec.SubnetName()
		}

		if srv.VPCAttachment == nil {
			slog.Info("Skipping server (no VPC attachment)...", "server", server.Name)
			continue
		}

		for _, some := range vpcList.Items {
			if srv.VPCAttachment.Spec.VPCName() != some.Name {
				continue
			}

			if some.Spec.Subnets[srv.VPCAttachment.Spec.SubnetName()] == nil {
				return errors.Errorf("VPC attachment subnet not found for server %s, attachment %s", srv.Name, srv.VPCAttachment.Name)
			}

			someCopy := some
			srv.VPC = &someCopy
		}

		out, err := svc.ssh(ctx, srv, "ip a s | grep 'inet 10\\.' | awk '/inet / {print $2}'", 0)
		if err != nil {
			return errors.Wrapf(err, "error getting IP for server %s", srv.Name)
		}

		ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(out))
		if err != nil {
			return errors.Wrapf(err, "error parsing IP for server %s", srv.Name)
		}

		if ipNet.String() != srv.VPC.Spec.Subnets[srv.Subnet].Subnet {
			return errors.Errorf("server %s IP %s doesn't match VPC subnet %s", srv.Name, ipNet.String(), srv.VPC.Spec.Subnets[srv.Subnet].Subnet)
		}

		srv.IP = ip.String()

		slog.Info("Found", "server", srv.Name, "conn", srv.ConnectionType, "switches", srv.ConnectedTo,
			"vpc", srv.VPC.Name, "subnet", srv.Subnet+":"+srv.VPC.Spec.Subnets[srv.Subnet].Subnet, "ip", srv.IP)

		servers[server.Name] = srv
	}

	sortedServer := []string{}
	for _, server := range servers {
		sortedServer = append(sortedServer, server.Name)
	}
	slices.Sort(sortedServer)

	for _, peering := range vpcPeeringList.Items {
		vpc1, vpc2, err := peering.Spec.VPCs()
		if err != nil {
			return errors.Wrapf(err, "error getting VPCs for peering %s", peering.Name)
		}

		vpc1Servers := []string{}
		vpc2Servers := []string{}
		for _, server := range servers {
			if server.VPC.Name == vpc1 {
				vpc1Servers = append(vpc1Servers, server.Name)
			}

			if server.VPC.Name == vpc2 {
				vpc2Servers = append(vpc2Servers, server.Name)
			}
		}

		if len(vpc1Servers) < 1 {
			return errors.Errorf("not enough servers found for peering %s for vpc %s", peering.Name, vpc1)
		}
		if len(vpc2Servers) < 1 {
			return errors.Errorf("not enough servers found for peering %s for vpc %s", peering.Name, vpc2)
		}

		for _, server1 := range vpc1Servers {
			for _, server2 := range vpc2Servers {
				if !slices.Contains(servers[server1].VPCPeers, server2) {
					servers[server1].VPCPeers = append(servers[server1].VPCPeers, server2)
				}

				if !slices.Contains(servers[server2].VPCPeers, server1) {
					servers[server2].VPCPeers = append(servers[server2].VPCPeers, server1)
				}
			}
		}
	}

	for _, peering := range externalPeeringList.Items {
		vpc := peering.Spec.Permit.VPC.Name
		subnets := peering.Spec.Permit.VPC.Subnets

		includeDefault := false
		for _, prefix := range peering.Spec.Permit.External.Prefixes {
			if prefix.Prefix == "0.0.0.0/0" {
				includeDefault = true
				break
			}
		}

		if !includeDefault {
			return errors.Errorf("external peering %s doesn't include default route, not supported for testing", peering.Name)
		}

		for _, server := range servers {
			if server.VPC.Name != vpc {
				continue
			}

			for _, subnet := range subnets {
				if server.Subnet != subnet {
					continue
				}

				if !slices.Contains(server.Externals, peering.Spec.Permit.External.Name) {
					if server.ExternalPeering != nil {
						return errors.Errorf("server %s has multiple external peerings, not supported for testing", server.Name)
					}
					peeringCopy := peering
					server.ExternalPeering = &peeringCopy
					server.Externals = append(server.Externals, peering.Spec.Permit.External.Name)
				}
			}
		}
	}

	totalTested := 0
	totalPassed := 0

	for _, name := range sortedServer {
		server := servers[name]
		slices.Sort(server.VPCPeers)

		slog.Info("To be tested", "server", server.Name, "vpcPeers", server.VPCPeers, "externals", server.Externals)

		if cfg.VPC {
			for _, vpcPeer := range sortedServer {
				if name == vpcPeer {
					continue
				}

				passed := true

				totalTested += 1

				peerConnected := slices.Contains(server.VPCPeers, vpcPeer)

				if cfg.VPCPing > 0 {
					cmd := fmt.Sprintf("ping -c %d -W 1 %s", cfg.VPCPing, servers[vpcPeer].IP)
					slog.Debug("Testing connectivity using ping", "from", name, "to", vpcPeer, "connected", peerConnected, "cmd", cmd)

					out, err := svc.ssh(ctx, server, cmd, int64(cfg.VPCPing)+5)

					failed := false
					if peerConnected && err != nil {
						passed = false

						slog.Error("Connectivity expected, ping failed", "from", server.Name, "to", vpcPeer, "err", err)
						failed = true
					} else if !peerConnected && err == nil {
						passed = false

						slog.Error("Connectivity not expected, ping not failed", "from", server.Name, "to", vpcPeer)
						failed = true
					} else if !peerConnected && err != nil && len(out) > 0 && !strings.Contains(out, "100% packet loss") {
						passed = false

						slog.Error("Connectivity not expected, ping failed without '100% packet loss' message", "from", server.Name, "to", vpcPeer, "err", err)
						failed = true
					} else if peerConnected {
						slog.Info("Connectivity expected, ping succeeded", "from", server.Name, "to", vpcPeer)
					} else if !peerConnected {
						slog.Info("Connectivity not expected, ping failed", "from", server.Name, "to", vpcPeer)
					} else {
						return errors.Errorf("unexpected result")
					}

					if slog.Default().Enabled(ctx, slog.LevelDebug) || failed {
						out = strings.TrimSpace(string(out))
						if failed {
							color.Red(out)
						} else {
							color.Green(out)
						}
					}
				}

				if peerConnected && cfg.VPCIperf > 0 {
					cmd := fmt.Sprintf("toolbox -q timeout %d iperf3 -J -c %s -t %d", cfg.VPCIperf+5, servers[vpcPeer].IP, cfg.VPCIperf)
					slog.Debug("Testing connectivity using iperf", "from", name, "to", vpcPeer, "connected", peerConnected, "cmd", cmd)

					wg := sync.WaitGroup{}
					wg.Add(2)

					go func() {
						defer wg.Done()

						cmd := fmt.Sprintf("toolbox -q timeout %d iperf3 -s -1", cfg.VPCIperf+7)
						slog.Debug("Starting iperf server", "host", vpcPeer, "cmd", cmd)

						// TODO use Cmd directly to start but not wait for it to finish
						out, err := svc.ssh(ctx, servers[vpcPeer], cmd, int64(cfg.VPCIperf)+10)
						if err != nil {
							passed = false

							slog.Error("Error starting iperf server", "host", vpcPeer, "err", err)
							color.Yellow(strings.TrimSpace(out))
							return
						} else {
							slog.Debug("iperf server output", "host", vpcPeer)

							if slog.Default().Enabled(ctx, slog.LevelDebug) {
								color.Cyan(strings.TrimSpace(out))
							}
						}
					}()

					go func() {
						defer wg.Done()

						time.Sleep(2 * time.Second) // TODO think about more reliable way to wait for server to start

						out, err := svc.ssh(ctx, server, cmd, int64(cfg.VPCIperf)+10)
						if err != nil {
							passed = false

							slog.Error("Connectivity expected, iperf failed", "from", server.Name, "to", vpcPeer, "err", err)
							color.Red(strings.TrimSpace(out)) // TODO think about parsing output and printing only summary
							return
						} else {
							report, err := parseIperf3Report(string(out))
							if err != nil {
								passed = false

								slog.Error("Error parsing iperf report", "err", err)
								return
							}

							slog.Info("iperf3 report", "host", name,
								"sentSpeed", humanize.Bytes(uint64(report.End.SumSent.BitsPerSecond/8))+"/s",
								"receivedSpeed", humanize.Bytes(uint64(report.End.SumReceived.BitsPerSecond/8))+"/s",
								"sent", humanize.Bytes(uint64(report.End.SumSent.Bytes)),
								"received", humanize.Bytes(uint64(report.End.SumReceived.Bytes)),
							)

							if report.End.SumSent.BitsPerSecond < 8500000000 { // TODO make configurable
								passed = false

								slog.Error("Connectivity expected, iperf speed too low", "from", server.Name, "to", vpcPeer, "speed", humanize.Bytes(uint64(report.End.SumSent.BitsPerSecond/8))+"/s")
							} else {
								slog.Info("Connectivity expected, iperf succeeded", "from", server.Name, "to", vpcPeer)
							}
						}
					}()

					wg.Wait()
				}

				if passed {
					totalPassed += 1
				}
			}
		}

		if cfg.Ext {
			for _, external := range server.Externals {
				if cfg.ExtCurl {
					totalTested += 1

					cmd := "toolbox -q timeout 5 curl --insecure https://8.8.8.8" // TODO make configurable
					slog.Debug("Testing external connectivity using curl", "from", name, "to", external, "cmd", cmd)

					out, err := svc.ssh(ctx, server, cmd, 10)
					if err != nil {
						slog.Error("External connectivity expected, curl failed", "from", server.Name, "to", external, "err", err)
						color.Red(strings.TrimSpace(out))
					} else {
						if !strings.Contains(out, "302 Moved") {
							slog.Error("External connectivity expected, curl succeeded but doesn't contain 302 Moved", "from", server.Name, "to", external)
							color.Red(strings.TrimSpace(out))
						} else {
							totalPassed += 1

							slog.Info("External connectivity expected, curl succeeded", "from", server.Name, "to", external)
							if slog.Default().Enabled(ctx, slog.LevelDebug) {
								color.Green(strings.TrimSpace(out))
							}
						}
					}
				}
			}
		}
	}

	slog.Info("Connectivity test complete", "tested", totalTested, "passed", totalPassed, "failed", totalTested-totalPassed, "took", time.Since(start))

	if totalTested-totalPassed > 0 {
		os.Exit(1)
	}

	return nil
}

type Server struct {
	Name string
	VM   *VM

	ConnectedTo    []string
	ConnectionType string

	Server          *wiringapi.Server
	Connection      *wiringapi.Connection
	VPCAttachment   *vpcapi.VPCAttachment
	VPC             *vpcapi.VPC
	Subnet          string
	ExternalPeering *vpcapi.ExternalPeering

	VPCPeers  []string
	Externals []string

	IP string
}

func (svc *Service) ssh(ctx context.Context, server *Server, cmd string, timeout int64) (string, error) {
	if timeout == 0 {
		timeout = 5
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	auth, err := goph.Key(svc.cfg.SshKey, "")
	if err != nil {
		return "", errors.Wrapf(err, "error loading SSH key")
	}

	client, err := goph.NewConn(&goph.Config{
		User:     "core",
		Addr:     "127.0.0.1",
		Port:     uint(server.VM.sshPort()),
		Auth:     auth,
		Timeout:  30 * time.Second,
		Callback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		return "", errors.Wrapf(err, "error creating SSH client")
	}

	out, err := client.RunContext(ctx, cmd)
	if err != nil {
		return string(out), errors.Wrapf(err, "error running command on server %s using ssh", server.Name)
	}

	return string(out), nil
}

type Iperf3Report struct {
	Intervals []Iperf3ReportInterval `json:"intervals"`
	End       Iperf3ReportEnd        `json:"end"`
}

type Iperf3ReportInterval struct {
	Sum Iperf3ReportSum `json:"sum"`
}

type Iperf3ReportEnd struct {
	SumSent     Iperf3ReportSum `json:"sum_sent"`
	SumReceived Iperf3ReportSum `json:"sum_received"`
}

type Iperf3ReportSum struct {
	Bytes         int64   `json:"bytes"`
	BitsPerSecond float64 `json:"bits_per_second"`
}

func parseIperf3Report(data string) (*Iperf3Report, error) {
	report := &Iperf3Report{}
	if err := json.Unmarshal([]byte(data), report); err != nil {
		return nil, errors.Wrapf(err, "error unmarshaling iperf3 report")
	}

	return report, nil
}

type TestScenarioSetupConfig struct {
	DryRun     bool
	CleanupAll bool
	Requests   []string
}

// TODO move vpc creation to here, just have flag --vpc-per-server
func (svc *Service) SetupTestScenario(ctx context.Context, cfg TestScenarioSetupConfig) error {
	start := time.Now()

	slog.Info("Setting up test scenario", "dryRun", cfg.DryRun, "numRequests", len(cfg.Requests))

	os.Setenv("KUBECONFIG", filepath.Join(svc.cfg.Basedir, "kubeconfig.yaml"))
	kube, err := kubeClient()
	if err != nil {
		return errors.Wrapf(err, "error creating kube client")
	}

	if err := checkAgents(ctx, kube); err != nil {
		return errors.Wrapf(err, "error checking agents")
	}

	externalList := &vpcapi.ExternalList{}
	if err := kube.List(ctx, externalList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing externals")
	}

	switchGroupList := &wiringapi.SwitchGroupList{}
	if err := kube.List(ctx, switchGroupList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing switch groups")
	}

	vpcPeerings := map[string]*vpcapi.VPCPeeringSpec{}
	externalPeerings := map[string]*vpcapi.ExternalPeeringSpec{}

	reqNames := map[string]bool{}
	for _, req := range cfg.Requests {
		parts := strings.Split(req, ":")
		if len(parts) < 1 {
			return errors.Errorf("invalid request format")
		}

		reqName := parts[0]
		if reqNames[reqName] {
			return errors.Errorf("duplicate request name %s", reqName)
		}
		reqNames[reqName] = true

		slog.Debug("Parsing request", "name", reqName, "options", parts[1:])

		vpMark := strings.Contains(reqName, "+")
		epMark := strings.Contains(reqName, "~")

		if vpMark && !epMark {
			reqNameParts := strings.Split(reqName, "+")
			if len(reqNameParts) != 2 {
				return errors.Errorf("invalid VPC peering request %s", reqName)
			}

			slices.Sort(reqNameParts)

			vpc1 := reqNameParts[0]
			vpc2 := reqNameParts[1]

			if vpc1 == "" || vpc2 == "" {
				return errors.Errorf("invalid VPC peering request %s, both VPCs should be non-empty", reqName)
			}

			if !strings.HasPrefix(vpc1, "vpc-") {
				vpc1 = "vpc-" + vpc1
			}
			if !strings.HasPrefix(vpc2, "vpc-") {
				vpc2 = "vpc-" + vpc2
			}

			vpcPeering := &vpcapi.VPCPeeringSpec{
				Permit: []map[string]vpcapi.VPCPeer{
					{
						vpc1: {},
						vpc2: {},
					},
				},
			}

			for idx, option := range parts[1:] {
				parts := strings.Split(option, "=")
				if len(parts) > 2 {
					return errors.Errorf("invalid VPC peering option #%d %s", idx, option)
				}

				optName := parts[0]
				optValue := ""
				if len(parts) == 2 {
					optValue = parts[1]
				}

				if optName == "r" || optName == "remote" {
					if optValue == "" {
						if len(switchGroupList.Items) != 1 {
							return errors.Errorf("invalid VPC peering option #%d %s, auto switch group only supported when it's exactly one switch group", idx, option)
						}

						vpcPeering.Remote = switchGroupList.Items[0].Name
					}

					vpcPeering.Remote = optValue
				} else {
					return errors.Errorf("invalid VPC peering option #%d %s", idx, option)
				}
			}

			vpcPeerings[fmt.Sprintf("%s--%s", vpc1, vpc2)] = vpcPeering
		} else if !vpMark && epMark {
			reqNameParts := strings.Split(reqName, "~")
			if len(reqNameParts) != 2 {
				return errors.Errorf("invalid external peering request %s", reqName)
			}

			vpc := reqNameParts[0]
			ext := reqNameParts[1]

			if vpc == "" {
				return errors.Errorf("invalid external peering request %s, VPC should be non-empty", reqName)
			}
			if ext == "" {
				return errors.Errorf("invalid external peering request %s, external should be non-empty", reqName)
			}

			if !strings.HasPrefix(vpc, "vpc-") {
				vpc = "vpc-" + vpc
			}

			extPeering := &vpcapi.ExternalPeeringSpec{
				Permit: vpcapi.ExternalPeeringSpecPermit{
					VPC: vpcapi.ExternalPeeringSpecVPC{
						Name:    vpc,
						Subnets: []string{},
					},
					External: vpcapi.ExternalPeeringSpecExternal{
						Name:     ext,
						Prefixes: []vpcapi.ExternalPeeringSpecPrefix{},
					},
				},
			}

			for idx, option := range parts[1:] {
				parts := strings.Split(option, "=")
				if len(parts) > 2 {
					return errors.Errorf("invalid VPC peering option #%d %s", idx, option)
				}

				optName := parts[0]
				optValue := ""
				if len(parts) == 2 {
					optValue = parts[1]
				}

				if optName == "vpc_subnets" || optName == "subnets" {
					if optValue == "" {
						return errors.Errorf("invalid external peering option #%d %s, VPC subnet names should be non-empty", idx, option)
					}

					extPeering.Permit.VPC.Subnets = append(extPeering.Permit.VPC.Subnets, strings.Split(optValue, ",")...)
				} else if optName == "ext_prefixes" || optName == "prefixes" {
					if optValue == "" {
						return errors.Errorf("invalid external peering option #%d %s, external prefixes should be non-empty", idx, option)
					}

					for _, rawPrefix := range strings.Split(optValue, ",") {
						prefix := vpcapi.ExternalPeeringSpecPrefix{
							Prefix: rawPrefix,
						}
						if strings.Contains(rawPrefix, "_") {
							prefixParts := strings.Split(rawPrefix, "_")
							if len(prefixParts) > 3 {
								return errors.Errorf("invalid external peering option #%d %s, external prefix should be in format prefix_leXX_geYY", idx, option)
							}

							prefix.Prefix = prefixParts[0]

							for _, prefixPart := range prefixParts[1:] {
								if strings.HasPrefix(prefixPart, "le") {
									le, err := strconv.Atoi(strings.TrimPrefix(prefixPart, "le"))
									if err != nil {
										return errors.Errorf("invalid external peering option #%d %s, external prefix should be in format prefix_leXX_geYY", idx, option)
									}

									prefix.Le = uint8(le)
								} else if strings.HasPrefix(prefixPart, "ge") {
									ge, err := strconv.Atoi(strings.TrimPrefix(prefixPart, "ge"))
									if err != nil {
										return errors.Errorf("invalid external peering option #%d %s, external prefix should be in format prefix_leXX_geYY", idx, option)
									}

									prefix.Ge = uint8(ge)
								} else {
									return errors.Errorf("invalid external peering option #%d %s, external prefix should be in format prefix_leXX_geYY", idx, option)
								}
							}
						}

						extPeering.Permit.External.Prefixes = append(extPeering.Permit.External.Prefixes, prefix)
					}
				} else {
					return errors.Errorf("invalid external peering option #%d %s", idx, option)
				}
			}

			if len(extPeering.Permit.VPC.Subnets) == 0 {
				extPeering.Permit.VPC.Subnets = []string{"default"}
			}
			slices.Sort(extPeering.Permit.VPC.Subnets)

			if len(extPeering.Permit.External.Prefixes) == 0 {
				extPeering.Permit.External.Prefixes = []vpcapi.ExternalPeeringSpecPrefix{
					{
						Prefix: "0.0.0.0/0",
						Le:     32,
					},
				}
			}
			slices.SortFunc(extPeering.Permit.External.Prefixes, func(a, b vpcapi.ExternalPeeringSpecPrefix) int {
				return strings.Compare(a.Prefix, b.Prefix)
			})

			externalPeerings[fmt.Sprintf("%s--%s", vpc, ext)] = extPeering
		} else {
			return errors.Errorf("invalid request name %s", reqName)
		}
	}

	vpcPeeringList := &vpcapi.VPCPeeringList{}
	if err := kube.List(ctx, vpcPeeringList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing VPC peerings")
	}
	for _, peering := range vpcPeeringList.Items {
		if !cfg.CleanupAll && vpcPeerings[peering.Name] != nil {
			continue
		}

		slog.Info("Deleting existing VPC peering", "name", peering.Name)

		if cfg.DryRun {
			continue
		}

		if err := client.IgnoreNotFound(kube.Delete(ctx, &peering)); err != nil {
			return errors.Wrapf(err, "error deleting VPC peering %s", peering.Name)
		}
	}

	externalPeeringList := &vpcapi.ExternalPeeringList{}
	if err := kube.List(ctx, externalPeeringList, client.InNamespace("default")); err != nil {
		return errors.Wrapf(err, "error listing external peerings")
	}
	for _, peering := range externalPeeringList.Items {
		if !cfg.CleanupAll && externalPeerings[peering.Name] != nil {
			continue
		}

		slog.Info("Deleting existing external peering", "name", peering.Name)

		if cfg.DryRun {
			continue
		}

		if err := client.IgnoreNotFound(kube.Delete(ctx, &peering)); err != nil {
			return errors.Wrapf(err, "error deleting external peering %s", peering.Name)
		}
	}

	for name, vpcPeeringSpec := range vpcPeerings {
		vpc1, vpc2, err := vpcPeeringSpec.VPCs()
		if err != nil {
			return errors.Wrapf(err, "error getting VPCs for peering %s", name)
		}

		slog.Info("Enforcing VPC Peering", "name", name,
			"vpc1", vpc1, "vpc2", vpc2, "remote", vpcPeeringSpec.Remote)

		if cfg.DryRun {
			continue
		}

		vpcPeering := &vpcapi.VPCPeering{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
		}
		if _, err := ctrlutil.CreateOrUpdate(ctx, kube, vpcPeering, func() error {
			vpcPeering.Spec = *vpcPeeringSpec

			return nil
		}); err != nil {
			return errors.Wrapf(err, "error updating VPC peering %s", name)
		}
	}

	for name, extPeeringSpec := range externalPeerings {
		slog.Info("Enforcing External Peering", "name", name,
			"vpc", extPeeringSpec.Permit.VPC.Name, "vpcSubnets", extPeeringSpec.Permit.VPC.Subnets,
			"external", extPeeringSpec.Permit.External.Name, "externalPrefixes", extPeeringSpec.Permit.External.Prefixes)

		if cfg.DryRun {
			continue
		}

		extPeering := &vpcapi.ExternalPeering{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
		}
		if _, err := ctrlutil.CreateOrUpdate(ctx, kube, extPeering, func() error {
			extPeering.Spec = *extPeeringSpec

			return nil
		}); err != nil {
			return errors.Wrapf(err, "error updating external")
		}
	}

	slog.Info("Test scenario setup complete", "took", time.Since(start))

	return nil
}
