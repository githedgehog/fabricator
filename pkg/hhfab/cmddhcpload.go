// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/hhfab/dhcpload"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var ErrDHCPLoadFailed = errors.New("DHCP load test failed")

const (
	// paths on the server in access mode, relative ones are in the home directory of the ssh user
	dhcpLoadAgentBin = "hhfab-dhcp-load"
	dhcpLoadAgentCfg = "hhfab-dhcp-load.json"
	dhcpLoadAgentCSV = "/tmp/hhfab-dhcp-load.csv"
)

type DHCPLoadOpts struct {
	dhcpload.Config
	FakeProtocolBase string
	FakeVTEPBase     string // empty = no VTEP IPs
	FakeASNBase      uint32
	// Server is the VLAB server running the clients in access mode, the first one with an unbundled connection if empty
	Server string
	// Keep skips the cleanup of the created objects and the network namespace
	Keep bool
	// SkipReadyCheck skips waiting for the fabric to be ready before the test
	SkipReadyCheck bool
}

func DoVLABDHCPLoad(ctx context.Context, workDir, cacheDir string, opts DHCPLoadOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.DHCPLoad(ctx, vlab, opts)
}

// RunDHCPLoadAgent runs the clients of a test, it's what DHCPLoad starts on the server in access mode. The config is
// the JSON of dhcpload.Config.
func RunDHCPLoadAgent(ctx context.Context, cfgPath string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg := dhcpload.DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	res, err := dhcpload.Run(ctx, cfg)
	if err != nil {
		return fmt.Errorf("running load test: %w", err)
	}
	if !res.OK {
		return resultErr(res)
	}

	return nil
}

func resultErr(res *dhcpload.Result) error {
	return fmt.Errorf("%w: clients=%d bound=%d failed=%d renews_failed=%d duplicate_ips=%d", ErrDHCPLoadFailed,
		res.Clients, res.Bound, res.Failed, res.RenewsFailed, res.DuplicateIPs)
}

//nolint:gocyclo,funlen
func (c *Config) DHCPLoad(ctx context.Context, vlab *VLAB, opts DHCPLoadOpts) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := &opts.Config
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validating config: %w", err)
	}
	access := cfg.Mode == dhcpload.ModeAccess
	if !access && os.Geteuid() != 0 {
		return fmt.Errorf("must be run as root to create the network namespace and bind the DHCP relay port") //nolint:err113
	}

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	existing := &vpcapi.VPC{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: cfg.VPCName(), Namespace: kmetav1.NamespaceDefault}, existing); err == nil {
		if existing.Labels[dhcpload.FakeLabel] == "" {
			return fmt.Errorf("VPC %s already exists and wasn't created by this command, use a different --vpc", cfg.VPCName()) //nolint:err113
		}
	} else if kclient.IgnoreNotFound(err) != nil {
		return fmt.Errorf("getting VPC %s: %w", cfg.VPCName(), err)
	}

	// leftovers from an earlier run with --keep would keep the fabric not ready
	if err := removeDHCPLoadObjects(ctx, kube); err != nil {
		return err
	}

	if !opts.SkipReadyCheck {
		if err := WaitReady(ctx, kube, WaitReadyOpts{Timeout: 10 * time.Minute}); err != nil {
			return fmt.Errorf("waiting for fabric ready: %w", err)
		}
	}

	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}

	var leaf *wiringapi.Switch
	var conn *wiringapi.Connection
	var server string
	var fakes []*wiringapi.Switch
	var controlPrefix netip.Prefix
	if access {
		conn, server, leaf, err = pickAccessServer(ctx, kube, switches.Items, opts.Server)
		if err != nil {
			return err
		}
		if cfg.Iface == "" {
			cfg.Iface = conn.Spec.Unbundled.Link.Server.LocalPortName()
		}
		slog.Info("Using server", "server", server, "iface", cfg.Iface, "connection", conn.Name, "leaf", leaf.Name)
	} else {
		leaf, fakes, controlPrefix, err = c.prepareRelays(&opts, switches.Items)
		if err != nil {
			return err
		}
	}

	if err := checkDHCPLoadNamespaces(ctx, kube, cfg, leaf); err != nil {
		return err
	}

	// from here on the cluster is changed
	if !opts.Keep {
		defer func() {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer cancel()
			slog.Info("Cleaning up")
			if err := removeDHCPLoadObjects(cctx, kube); err != nil {
				slog.Error("Cleaning up failed", "err", err)
			}
		}()
	}

	if !access {
		slog.Info("Creating fake leaf switches", "count", len(fakes), "template", leaf.Name)
		for _, fake := range fakes {
			some := &wiringapi.Switch{ObjectMeta: kmetav1.ObjectMeta{Name: fake.Name, Namespace: fake.Namespace}}
			if _, err := ctrlutil.CreateOrUpdate(ctx, kube, some, func() error {
				some.Labels = fake.Labels
				some.Spec = fake.Spec
				some.Default()

				return nil
			}); err != nil {
				return fmt.Errorf("creating fake switch %s: %w", fake.Name, err)
			}
		}
	}

	vpc := dhcpload.BuildVPC(cfg)
	slog.Info("Creating VPC", "name", vpc.Name, "subnets", len(vpc.Spec.Subnets))
	someVPC := &vpcapi.VPC{ObjectMeta: kmetav1.ObjectMeta{Name: vpc.Name, Namespace: vpc.Namespace}}
	if _, err := ctrlutil.CreateOrUpdate(ctx, kube, someVPC, func() error {
		someVPC.Labels = vpc.Labels
		someVPC.Spec = vpc.Spec
		someVPC.Default()

		return nil
	}); err != nil {
		return fmt.Errorf("creating VPC %s: %w", vpc.Name, err)
	}

	if access {
		if err := attachAccessServer(ctx, kube, cfg, conn, leaf.Name); err != nil {
			return err
		}
	}

	if err := waitDHCPSubnets(ctx, kube, cfg); err != nil {
		return err
	}

	if access {
		return c.runAccess(ctx, vlab, opts, server)
	}

	return runRelay(ctx, opts, controlPrefix)
}

// prepareRelays picks the leaf to clone, builds the fake switches and checks them and the relay IPs against the
// management network and the real switches
func (c *Config) prepareRelays(opts *DHCPLoadOpts, switches []wiringapi.Switch) (*wiringapi.Switch, []*wiringapi.Switch, netip.Prefix, error) {
	cfg := &opts.Config
	if len(c.Controls) == 0 {
		return nil, nil, netip.Prefix{}, fmt.Errorf("no control node in the config") //nolint:err113
	}

	fakeOpts := dhcpload.FakeSwitchOpts{ASNBase: opts.FakeASNBase}
	var err error
	if fakeOpts.ProtocolBase, err = netip.ParseAddr(opts.FakeProtocolBase); err != nil {
		return nil, nil, netip.Prefix{}, fmt.Errorf("parsing fake protocol base: %w", err)
	}
	if opts.FakeVTEPBase != "" {
		if fakeOpts.VTEPBase, err = netip.ParseAddr(opts.FakeVTEPBase); err != nil {
			return nil, nil, netip.Prefix{}, fmt.Errorf("parsing fake VTEP base: %w", err)
		}
	}

	controlPrefix, err := c.Controls[0].Spec.Management.IP.Parse()
	if err != nil {
		return nil, nil, netip.Prefix{}, fmt.Errorf("parsing control node management IP: %w", err)
	}
	mgmtPrefix, err := c.Fab.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return nil, nil, netip.Prefix{}, fmt.Errorf("parsing management subnet: %w", err)
	}
	dhcpStart, _ := c.Fab.Spec.Config.Fabric.ManagementDHCPStart.Parse()
	dhcpEnd, _ := c.Fab.Spec.Config.Fabric.ManagementDHCPEnd.Parse()
	if err := dhcpload.CheckManagement(cfg.RelayIPs(), mgmtPrefix, dhcpStart, dhcpEnd, controlPrefix.Addr()); err != nil {
		return nil, nil, netip.Prefix{}, fmt.Errorf("checking relay IPs: %w", err)
	}
	cfg.Server = netip.AddrPortFrom(controlPrefix.Addr(), 67)

	var template *wiringapi.Switch
	for _, sw := range switches {
		if sw.Spec.Role.IsLeaf() && sw.Spec.IP != "" {
			template = sw.DeepCopy()

			break
		}
	}
	if template == nil {
		return nil, nil, netip.Prefix{}, fmt.Errorf("no leaf switch found to clone fake leaves from") //nolint:err113
	}

	fakes, err := dhcpload.BuildFakeSwitches(cfg, template, fakeOpts)
	if err != nil {
		return nil, nil, netip.Prefix{}, fmt.Errorf("building fake switches: %w", err)
	}
	if err := dhcpload.CheckSwitchConflicts(fakes, switches); err != nil {
		return nil, nil, netip.Prefix{}, fmt.Errorf("checking fake switches: %w", err)
	}

	return template, fakes, controlPrefix, nil
}

// runRelay runs the emulated relays and clients from a network namespace attached to the VLAB management bridge
func runRelay(ctx context.Context, opts DHCPLoadOpts, controlPrefix netip.Prefix) error {
	cfg := &opts.Config

	slog.Info("Setting up network namespace", "name", dhcpload.DefaultNetNS, "bridge", VLABBridge, "relays", cfg.Leaves)
	ns, err := dhcpload.SetupNetNS(dhcpload.DefaultNetNS, VLABBridge, cfg.RelayIPs(), controlPrefix.Bits())
	if err != nil {
		return fmt.Errorf("setting up netns: %w", err)
	}
	if !opts.Keep {
		defer func() {
			if err := ns.Cleanup(); err != nil {
				slog.Error("Removing netns failed", "err", err)
			}
		}()
	}

	if err := ns.CheckReachable(ctx, netip.AddrPortFrom(controlPrefix.Addr(), 22), 10*time.Second); err != nil {
		return fmt.Errorf("control node unreachable (is the VLAB management bridge forwarding? a Docker FORWARD DROP policy breaks it): %w", err)
	}

	var res *dhcpload.Result
	if err := ns.Do(func() error {
		var err error
		res, err = dhcpload.Run(ctx, cfg)

		return err //nolint:wrapcheck
	}); err != nil {
		return fmt.Errorf("running load test: %w", err)
	}
	if !res.OK {
		return resultErr(res)
	}

	return nil
}

// pickAccessServer finds the unbundled connection (and the leaf it's on) of the requested server, or of the first one
func pickAccessServer(ctx context.Context, kube kclient.Reader, switches []wiringapi.Switch, want string) (*wiringapi.Connection, string, *wiringapi.Switch, error) {
	conns := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, conns); err != nil {
		return nil, "", nil, fmt.Errorf("listing connections: %w", err)
	}
	slices.SortFunc(conns.Items, func(a, b wiringapi.Connection) int { return cmp.Compare(a.Name, b.Name) })

	for _, conn := range conns.Items {
		if conn.Spec.Unbundled == nil {
			continue
		}
		server := conn.Spec.Unbundled.Link.Server.DeviceName()
		if want != "" && server != want {
			continue
		}
		swName := conn.Spec.Unbundled.Link.Switch.DeviceName()
		for _, sw := range switches {
			if sw.Name == swName {
				return conn.DeepCopy(), server, sw.DeepCopy(), nil
			}
		}
	}

	if want != "" {
		return nil, "", nil, fmt.Errorf("no unbundled connection found for server %q", want) //nolint:err113
	}

	return nil, "", nil, fmt.Errorf("no server with an unbundled connection found") //nolint:err113
}

// attachAccessServer attaches every subnet of the VPC to the connection of the server with tagged VLANs and waits
// until the leaf applied the change, as it has to relay DHCP for the subnets before the clients start
func attachAccessServer(ctx context.Context, kube kclient.Client, cfg *dhcpload.Config, conn *wiringapi.Connection, leaf string) error {
	agent := &agentapi.Agent{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: leaf, Namespace: kmetav1.NamespaceDefault}, agent); err != nil {
		return fmt.Errorf("getting agent of %s: %w", leaf, err)
	}
	prevGen := agent.Generation

	slog.Info("Attaching VPC subnets", "connection", conn.Name, "count", cfg.NumSubnets())
	for i := range cfg.NumSubnets() {
		attach := &vpcapi.VPCAttachment{ObjectMeta: kmetav1.ObjectMeta{Name: cfg.DHCPSubnetName(i), Namespace: kmetav1.NamespaceDefault}}
		if _, err := ctrlutil.CreateOrUpdate(ctx, kube, attach, func() error {
			attach.Labels = map[string]string{dhcpload.FakeLabel: "true"}
			attach.Spec = vpcapi.VPCAttachmentSpec{
				Connection: conn.Name,
				Subnet:     fmt.Sprintf("%s/%s", cfg.VPCName(), dhcpload.SubnetName(i)),
			}
			attach.Default()

			return nil
		}); err != nil {
			return fmt.Errorf("creating VPC attachment %s: %w", attach.Name, err)
		}
	}

	slog.Info("Waiting for the leaf to apply the attachments", "leaf", leaf)
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		if err := kube.Get(waitCtx, kclient.ObjectKey{Name: leaf, Namespace: kmetav1.NamespaceDefault}, agent); err == nil &&
			agent.Generation > prevGen && agent.Status.LastAppliedGen == agent.Generation {
			return nil
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("waiting for agent %s to apply generation after %d: %w", leaf, prevGen, waitCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// runAccess runs the clients on the server, they send DHCP broadcasts over the server's NIC to the leaf, which relays them
func (c *Config) runAccess(ctx context.Context, vlab *VLAB, opts DHCPLoadOpts, server string) error {
	cfg := opts.Config

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting own path: %w", err)
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("stating %s: %w", exe, err)
	}

	ssh, err := c.SSH(ctx, vlab, server)
	if err != nil {
		return fmt.Errorf("getting ssh to %s: %w", server, err)
	}

	slog.Info("Preparing server", "server", server)
	// whatever hhnet configured earlier would own the NIC and its VLANs
	if _, stderr, err := ssh.Run(ctx, "sudo /opt/bin/hhnet cleanup"); err != nil {
		return fmt.Errorf("cleaning up network config on %s: %w: %s", server, err, stderr)
	}

	slog.Info("Uploading generator", "server", server, "size", exeInfo.Size())
	if err := ssh.UploadPath(exe, dhcpLoadAgentBin); err != nil {
		return fmt.Errorf("uploading %s to %s: %w", exe, server, err)
	}
	stdout, stderr, err := ssh.Run(ctx, "chmod +x "+dhcpLoadAgentBin+" && stat -c %s "+dhcpLoadAgentBin)
	if err != nil {
		return fmt.Errorf("checking uploaded generator: %w: %s", err, stderr)
	}
	if size, err := strconv.ParseInt(strings.TrimSpace(stdout), 10, 64); err != nil || size != exeInfo.Size() {
		return fmt.Errorf("uploaded generator is %q bytes, expected %d", strings.TrimSpace(stdout), exeInfo.Size()) //nolint:err113
	}

	cfg.Summary = nil
	if cfg.CSVPath != "" {
		cfg.CSVPath = dhcpLoadAgentCSV
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	tmp, err := os.CreateTemp("", "dhcp-load-*.json")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()

		return fmt.Errorf("writing config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing config: %w", err)
	}
	if err := ssh.UploadPath(tmp.Name(), dhcpLoadAgentCfg); err != nil {
		return fmt.Errorf("uploading config to %s: %w", server, err)
	}

	slog.Info("Running clients", "server", server, "clients", cfg.Servers*cfg.NICs)
	runErr := ssh.StreamLog(ctx, "sudo ./"+dhcpLoadAgentBin+" dhcp-load-agent --config "+dhcpLoadAgentCfg, server, slog.Info)
	if ctx.Err() != nil {
		// stop the generator so it removes its VLAN interfaces, the name is short enough to match exactly
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, _, err := ssh.Run(cctx, "sudo pkill -INT -x "+dhcpLoadAgentBin); err != nil {
			slog.Warn("Stopping the generator failed", "err", err)
		}
	}

	if opts.Config.CSVPath != "" {
		if err := ssh.DownloadPath(dhcpLoadAgentCSV, opts.Config.CSVPath); err != nil {
			slog.Warn("Downloading the per-client results failed", "err", err)
		} else {
			slog.Info("Per-client results written", "path", filepath.Clean(opts.Config.CSVPath))
		}
	}

	if runErr != nil {
		return fmt.Errorf("%w: %w", ErrDHCPLoadFailed, runErr)
	}

	return nil
}

// checkDHCPLoadNamespaces makes sure the VPC subnets and VLANs fit the namespaces the VPC webhook checks them against
func checkDHCPLoadNamespaces(ctx context.Context, kube kclient.Reader, cfg *dhcpload.Config, leaf *wiringapi.Switch) error {
	var vlans []meta.VLANRange
	for _, name := range leaf.Spec.VLANNamespaces {
		ns := &wiringapi.VLANNamespace{}
		if err := kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, ns); err != nil {
			return fmt.Errorf("getting VLAN namespace %s: %w", name, err)
		}
		vlans = append(vlans, ns.Spec.Ranges...)
	}

	// the VPC uses the default IPv4 namespace, there is no per-leaf one
	v4 := &vpcapi.IPv4Namespace{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: "default", Namespace: kmetav1.NamespaceDefault}, v4); err != nil {
		return fmt.Errorf("getting IPv4 namespace default: %w", err)
	}
	ipv4 := make([]netip.Prefix, 0, len(v4.Spec.Subnets))
	for _, s := range v4.Spec.Subnets {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return fmt.Errorf("parsing IPv4 namespace subnet %q: %w", s, err)
		}
		ipv4 = append(ipv4, p)
	}

	return dhcpload.CheckNamespaces(cfg, ipv4, vlans) //nolint:wrapcheck
}

func waitDHCPSubnets(ctx context.Context, kube kclient.Reader, cfg *dhcpload.Config) error {
	want := cfg.NumSubnets()
	slog.Info("Waiting for DHCPSubnets", "count", want)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	for {
		missing := []int{}
		for i := range want {
			if err := kube.Get(ctx, kclient.ObjectKey{Name: cfg.DHCPSubnetName(i), Namespace: kmetav1.NamespaceDefault}, &dhcpapi.DHCPSubnet{}); err != nil {
				if kclient.IgnoreNotFound(err) != nil && ctx.Err() == nil {
					return fmt.Errorf("getting DHCPSubnet %s: %w", cfg.DHCPSubnetName(i), err)
				}
				missing = append(missing, i)
			}
		}
		if len(missing) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for DHCPSubnets, still missing %d (first: %s): %w", len(missing), cfg.DHCPSubnetName(slices.Min(missing)), ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// removeDHCPLoadObjects deletes the attachments, the VPC and the fake switches created by the test, if any
func removeDHCPLoadObjects(ctx context.Context, kube kclient.Client) error {
	for _, obj := range []kclient.Object{&vpcapi.VPCAttachment{}, &vpcapi.VPC{}, &wiringapi.Switch{}} {
		if err := kube.DeleteAllOf(ctx, obj, kclient.InNamespace(kmetav1.NamespaceDefault),
			kclient.MatchingLabels{dhcpload.FakeLabel: "true"}); err != nil {
			return fmt.Errorf("deleting %T: %w", obj, err)
		}
	}

	return nil
}
