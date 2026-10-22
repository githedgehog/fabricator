// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/api/valid"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	fabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/hhfab/bench"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// BenchInitOpts configures `hhfab vlab bench init`.
type BenchInitOpts struct {
	Fabrics []string
	Workers int
	QPS     float32
	Burst   int
	Phase   string
	Force   bool
	DryRun  bool
	Out     string
	// SkipValidate bypasses the local check, leaving it to the admission
	// webhooks. Worth it on a large run, where validating locally costs more
	// than the apply.
	SkipValidate bool
	// DefaultFabric puts every switch in the default fabric, without Fabric
	// objects or fabric and domain fields, so the topology can be created on a
	// release from before them and carried through the upgrade.
	DefaultFabric bool
}

// benchLoad loads only fab.yaml: the Fabricator config and the control and
// gateway nodes, which is everything the bench takes from the work dir. The
// VLAB's own wiring has nothing the bench uses, and loading it would validate
// it with the fabric API this binary was built with - which need not match the
// release the VLAB runs, the very case --default-fabric exists for.
func benchLoad(ctx context.Context, workDir, cacheDir string) (*Config, error) {
	return load(ctx, workDir, cacheDir, nil, false, HydrateModeNever, "")
}

// DoVLABBenchInit generates the benchmark topology and applies it to the VLAB
// control node.
func DoVLABBenchInit(ctx context.Context, workDir, cacheDir string, opts BenchInitOpts) error {
	specs, err := bench.ParseFabricSpecs(opts.Fabrics)
	if err != nil {
		return err //nolint:wrapcheck // already describes which --fabric failed
	}

	c, err := benchLoad(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	gen, l, err := benchGenerate(ctx, c, specs, opts.SkipValidate, opts.DefaultFabric)
	if err != nil {
		return err
	}

	summarize(specs, gen)

	if opts.DryRun {
		// Allowed, because it is the quick way to see what a large topology
		// would contain - local validation at that size takes hours. But nothing
		// has checked the result: skip-validate normally leaves that to the
		// admission webhooks, and a dry run never reaches them.
		if opts.SkipValidate {
			slog.Warn("Dry run with --skip-validate: the generated objects have not been validated by anything")
		}

		return benchDryRun(ctx, l, opts.Out)
	}

	kube, err := bench.NewKubeClient(ctx, filepath.Join(workDir, VLABDir, VLABKubeConfig), opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	if err := checkCoexistence(ctx, kube, opts.Force); err != nil {
		return err
	}

	start := time.Now()

	results, err := bench.Apply(ctx, kube, l, bench.ApplyOpts{
		Workers: opts.Workers,
		Phase:   opts.Phase,
	})

	reportPhases(results)

	if err != nil {
		return fmt.Errorf("applying: %w", err)
	}

	slog.Info("Benchmark topology applied", "took", time.Since(start).Truncate(time.Millisecond))

	return nil
}

// BenchAgentsOpts configures `hhfab vlab bench agents`.
type BenchAgentsOpts struct {
	Duration       time.Duration
	Interval       time.Duration
	ApplyDelay     time.Duration
	Agents         []string
	APIVia         string
	SyncHeartbeats bool
	PadSize        string
	QPS            float32
	Burst          int
}

// DoVLABBenchAgents simulates switch agents against the Agent objects in the
// cluster.
func DoVLABBenchAgents(ctx context.Context, workDir, cacheDir string, opts BenchAgentsOpts) error {
	padSize, err := bench.ParseSize(opts.PadSize)
	if err != nil {
		return err //nolint:wrapcheck
	}

	kubeconfig := filepath.Join(workDir, VLABDir, VLABKubeConfig)

	// Discovery needs admin credentials: the per-switch Role grants get and
	// watch on that switch's own Agent, with no list.
	admin, err := bench.NewKubeClient(ctx, kubeconfig, opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	apiServer := ""
	sourceAddr := netip.Addr{}
	switch opts.APIVia {
	case bench.APIViaBridgeIP, "":
		// The way real switches reach the control node: the control VIP over
		// the management bridge, rather than through the VM's user-mode
		// networking, which serialises every agent through one slirp loop and
		// has crashed QEMU when a large fleet's connections all close at once.
		addr, source, err := benchBridgeIP(ctx, workDir, cacheDir, k3s.APIPort)
		if err != nil {
			return err
		}

		slog.Info("Reaching the API over the management bridge", "bridge", VLABBridge, "source", source, "api", addr)

		apiServer, sourceAddr = "https://"+addr.String(), source
	case bench.APIViaHostfwd:
		// Each agent's own kubeconfig points at the control VIP on the
		// management network, which the host may have no address on. Reuse
		// whatever address the admin kubeconfig reaches the API by.
		cfg, err := kubeutil.NewClientConfig(ctx, kubeconfig)
		if err != nil {
			return fmt.Errorf("reading kubeconfig: %w", err)
		}

		apiServer = cfg.Host
	}

	if err := bench.RunAgents(ctx, admin, bench.AgentsOpts{
		Duration:       opts.Duration,
		Interval:       opts.Interval,
		ApplyDelay:     opts.ApplyDelay,
		Agents:         opts.Agents,
		APIVia:         opts.APIVia,
		APIServer:      apiServer,
		SourceAddr:     sourceAddr,
		SyncHeartbeats: opts.SyncHeartbeats,
		PadSize:        padSize,
	}); err != nil {
		return fmt.Errorf("running agents: %w", err)
	}

	return nil
}

// benchBridgeIP reaches a control node service the way real switches do: port
// on the control VIP from the Fabricator config, from the last address of the
// management subnet on the VLAB management bridge. The address is added to the
// bridge if it is not there yet, and the VIP is probed from it so a broken path
// fails here rather than as every simulated switch timing out on its own dial.
func benchBridgeIP(ctx context.Context, workDir, cacheDir string, port uint16) (netip.AddrPort, netip.Addr, error) {
	c, err := benchLoad(ctx, workDir, cacheDir)
	if err != nil {
		return netip.AddrPort{}, netip.Addr{}, err
	}

	vip, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return netip.AddrPort{}, netip.Addr{}, fmt.Errorf("parsing control VIP: %w", err)
	}
	subnet, err := c.Fab.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return netip.AddrPort{}, netip.Addr{}, fmt.Errorf("parsing management subnet: %w", err)
	}

	source, err := bench.LastHostAddr(subnet)
	if err != nil {
		return netip.AddrPort{}, netip.Addr{}, fmt.Errorf("picking an address on the management bridge: %w", err)
	}

	if err := ensureBridgeAddr(ctx, netip.PrefixFrom(source, subnet.Bits())); err != nil {
		return netip.AddrPort{}, netip.Addr{}, err
	}

	addr := netip.AddrPortFrom(vip.Addr(), port)

	dialer := &net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{IP: source.AsSlice()}}
	conn, err := dialer.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return netip.AddrPort{}, netip.Addr{}, fmt.Errorf("control VIP %s is not reachable from %s on %s: %w", addr, source, VLABBridge, err)
	}
	if err := conn.Close(); err != nil {
		return netip.AddrPort{}, netip.Addr{}, fmt.Errorf("closing probe connection to %s: %w", addr, err)
	}

	return addr, source, nil
}

// ensureBridgeAddr puts addr on the VLAB management bridge unless it is there
// already. Reading the bridge's addresses needs no privileges, so sudo is only
// run - and can only prompt - when something has to change.
func ensureBridgeAddr(ctx context.Context, addr netip.Prefix) error {
	iface, err := net.InterfaceByName(VLABBridge)
	if err != nil {
		return fmt.Errorf("getting management bridge %s, is the VLAB running: %w", VLABBridge, err)
	}

	existing, err := iface.Addrs()
	if err != nil {
		return fmt.Errorf("listing addresses on %s: %w", VLABBridge, err)
	}
	for _, a := range existing {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.Equal(addr.Addr().AsSlice()) {
			return nil
		}
	}

	slog.Info("Adding address to the management bridge", "bridge", VLABBridge, "addr", addr)

	cmd := exec.CommandContext(ctx, VLABCmdSudo, "ip", "addr", "add", addr.String(), "dev", VLABBridge) //nolint:gosec // addr is a parsed prefix, formatted by netip
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("adding %s to %s: %w", addr, VLABBridge, err)
	}

	return nil
}

// BenchBootsOpts configures `hhfab vlab bench boots`.
type BenchBootsOpts struct {
	Rounds      int
	Switches    []string
	LogLines    int
	InstallTime time.Duration
	RetryDelay  time.Duration
	MaxFailures int
	QPS         float32
	Burst       int
}

// DoVLABBenchBoots simulates every bench switch reinstalling itself from
// fabric-boot through ONIE, all at once.
func DoVLABBenchBoots(ctx context.Context, workDir, cacheDir string, opts BenchBootsOpts) error {
	admin, err := bench.NewKubeClient(ctx, filepath.Join(workDir, VLABDir, VLABKubeConfig), opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	// fabric-boot is on the control VIP, which switches reach over the
	// management network, so the bench comes from the management bridge too.
	addr, source, err := benchBridgeIP(ctx, workDir, cacheDir, fabcomp.BootPort)
	if err != nil {
		return err
	}

	slog.Info("Reaching fabric-boot over the management bridge", "bridge", VLABBridge, "source", source, "boot", addr)

	if err := bench.RunBoots(ctx, admin, bench.BootsOpts{
		URL:         "http://" + addr.String(),
		SourceAddr:  source,
		Switches:    opts.Switches,
		Rounds:      opts.Rounds,
		LogLines:    opts.LogLines,
		InstallTime: opts.InstallTime,
		RetryDelay:  opts.RetryDelay,
		MaxFailures: opts.MaxFailures,
	}); err != nil {
		return fmt.Errorf("running boots: %w", err)
	}

	return nil
}

// BenchDHCPOpts configures `hhfab vlab bench dhcp`.
type BenchDHCPOpts struct {
	Servers          int
	Subnets          int
	VPCs             int
	MaxSubnetServers int
	Rounds           int
	Repeats          int
	RepeatInterval   time.Duration
	Spread           time.Duration
	RoundTimeout     time.Duration
	PXE              bool
	Relay            string
	Sudo             bool
	QPS              float32
	Burst            int
}

// DoVLABBenchDHCP storms fabric-dhcpd with a fleet of servers booting at once,
// relayed from a bench leaf's management IP. Binding the relay's port 67 needs
// root, so without it this fails, or with Sudo runs itself again under sudo.
func DoVLABBenchDHCP(ctx context.Context, workDir, cacheDir string, opts BenchDHCPOpts) error {
	if os.Geteuid() != 0 {
		if !opts.Sudo {
			return fmt.Errorf("bench dhcp binds UDP port 67 as the relay, which needs root: run it with --sudo, or under sudo") //nolint:err113
		}

		return reexecUnderSudo(cacheDir)
	}

	c, err := benchLoad(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	vip, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control VIP: %w", err)
	}

	kube, err := bench.NewKubeClient(ctx, filepath.Join(workDir, VLABDir, VLABKubeConfig), opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	relay, err := bench.FindDHCPRelay(ctx, kube, opts.Relay)
	if err != nil {
		return err //nolint:wrapcheck
	}

	// The relay's address is a bench leaf's management IP: no real switch
	// holds it, so the host can, and dhcpd's replies to it come over the
	// management bridge like any other management traffic.
	if err := ensureBridgeAddr(ctx, relay.IP); err != nil {
		return err
	}

	server := netip.AddrPortFrom(vip.Addr(), bench.DHCPServerPort)

	transport, err := bench.ListenDHCPRelay(relay.IP.Addr(), server)
	if err != nil {
		return err //nolint:wrapcheck
	}
	defer transport.Close()

	slog.Info("Relaying to dhcpd over the management bridge", "bridge", VLABBridge, "relay", relay.Name, "giaddr", relay.IP.Addr(), "server", server)

	if err := bench.RunDHCP(ctx, kube, transport, bench.DHCPOpts{
		Layout:         bench.DHCPLayout{Servers: opts.Servers, Subnets: opts.Subnets, VPCs: opts.VPCs, MaxSubnetServers: opts.MaxSubnetServers},
		Rounds:         opts.Rounds,
		Repeats:        opts.Repeats,
		RepeatInterval: opts.RepeatInterval,
		Spread:         opts.Spread,
		RoundTimeout:   opts.RoundTimeout,
		PXE:            opts.PXE,
		Relay:          relay,
		Server:         server,
	}); err != nil {
		return fmt.Errorf("running dhcp storm: %w", err)
	}

	return nil
}

// reexecUnderSudo replaces this process with the same command under sudo,
// minus --sudo. The cache dir this process resolved is created here, as the
// user, and handed over explicitly, so root uses the same one rather than
// creating it as its own. HOME and HHFAB_* are kept so the preview gate and
// any other setting survive the switch.
func reexecUnderSudo(cacheDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding the hhfab binary: %w", err)
	}

	sudo, err := exec.LookPath(VLABCmdSudo)
	if err != nil {
		return fmt.Errorf("finding sudo: %w", err)
	}

	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return fmt.Errorf("creating cache dir %q: %w", cacheDir, err)
	}

	argv, env := sudoCommand(exe, os.Args[1:], os.Environ(), cacheDir)

	slog.Info("Running again under sudo to bind UDP port 67", "cacheDir", cacheDir)

	if err := syscall.Exec(sudo, argv, env); err != nil {
		return fmt.Errorf("running under sudo: %w", err)
	}

	return nil
}

// sudoCommand is the argv and environment to run exe with args under sudo:
// args without --sudo, and HHFAB_CACHE_DIR set to cacheDir, kept through sudo
// with HOME and every other HHFAB_* variable.
func sudoCommand(exe string, args, environ []string, cacheDir string) ([]string, []string) {
	env := make([]string, 0, len(environ)+1)
	keep := []string{"HOME"}

	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if ok && name == envCacheDir {
			continue
		}

		env = append(env, kv)
		if ok && strings.HasPrefix(name, "HHFAB_") {
			keep = append(keep, name)
		}
	}

	env = append(env, envCacheDir+"="+cacheDir)
	keep = append(keep, envCacheDir)

	argv := []string{VLABCmdSudo, "--preserve-env=" + strings.Join(keep, ","), exe}
	argv = append(argv, withoutSudoFlag(args)...)

	return argv, env
}

// envCacheDir sets the cache dir like --cache-dir does.
const envCacheDir = "HHFAB_CACHE_DIR"

// withoutSudoFlag drops --sudo in any of its spellings, so the command run
// under sudo doesn't try to re-run itself again.
func withoutSudoFlag(args []string) []string {
	out := make([]string, 0, len(args))

	for _, arg := range args {
		name := strings.TrimLeft(arg, "-")
		if strings.HasPrefix(arg, "-") && (name == "sudo" || strings.HasPrefix(name, "sudo=")) {
			continue
		}

		out = append(out, arg)
	}

	return out
}

// BenchUsersOpts configures `hhfab vlab bench users`.
type BenchUsersOpts struct {
	Duration         time.Duration
	UpdateWorkers    int
	UpdateSleep      time.Duration
	InspectWorkers   int
	InspectSleep     time.Duration
	Kinds            []string
	Inspects         []string
	Fabrics          []string
	OpTimeout        time.Duration
	InspectOneSwitch bool
	QPS              float32
	Burst            int
}

// DoVLABBenchUsers simulates operators changing objects and running inspect.
func DoVLABBenchUsers(ctx context.Context, workDir, cacheDir string, opts BenchUsersOpts) error {
	kubeconfig := filepath.Join(workDir, VLABDir, VLABKubeConfig)

	// Discovery only; every worker builds its own isolated client so the load
	// looks like separate operators rather than one process.
	admin, err := bench.NewKubeClient(ctx, kubeconfig, opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	if err := bench.RunUsers(ctx, admin, bench.UsersOpts{
		Kubeconfig:       kubeconfig,
		Duration:         opts.Duration,
		UpdateWorkers:    opts.UpdateWorkers,
		UpdateSleep:      opts.UpdateSleep,
		InspectWorkers:   opts.InspectWorkers,
		InspectSleep:     opts.InspectSleep,
		Kinds:            opts.Kinds,
		Inspects:         opts.Inspects,
		Fabrics:          opts.Fabrics,
		OpTimeout:        opts.OpTimeout,
		InspectOneSwitch: opts.InspectOneSwitch,
		QPS:              opts.QPS,
		Burst:            opts.Burst,
	}); err != nil {
		return fmt.Errorf("running users: %w", err)
	}

	return nil
}

// BenchSampleOpts configures `hhfab vlab bench sample`.
type BenchSampleOpts struct {
	Duration time.Duration
	Interval time.Duration
}

// DoVLABBenchSample records etcd and control node memory over time, to a file
// bench health reads back.
//
// It is deliberately its own command rather than part of a load generator: what
// it observes is a property of the cluster, not of any particular load, so it
// runs alongside agents, users, init, or nothing at all. Unlike health, where
// the control node is one section of several, it is the whole point here - so a
// missing one fails rather than quietly producing an empty series.
func DoVLABBenchSample(ctx context.Context, workDir, cacheDir string, opts BenchSampleOpts) error {
	// Checked before reaching for the control node, so a bad flag fails fast.
	if opts.Interval <= 0 {
		return fmt.Errorf("--interval must be positive, got %s", opts.Interval) //nolint:err113
	}

	run, err := controlNodeRunner(ctx, workDir, cacheDir)
	if err != nil {
		return fmt.Errorf("sampling needs the control node: %w", err)
	}

	// The VIP finds the management NIC on the control node; without it the
	// sample just leaves the NIC out.
	c, err := benchLoad(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	vip, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		slog.Warn("Not sampling the management NIC, no control VIP", "err", err)
	}

	// Always the work-dir file: that is where health looks, and a configurable
	// path would only produce a series health cannot find.
	path := filepath.Join(workDir, bench.SeriesFile)

	// A zero duration samples until interrupted, which is the common case when
	// it is started next to a run of unknown length.
	if opts.Duration > 0 {
		var stop context.CancelFunc

		ctx, stop = context.WithTimeout(ctx, opts.Duration)
		defer stop()
	}

	throttlePath := filepath.Join(workDir, bench.ThrottleFile)

	slog.Info("Sampling etcd and node memory",
		"interval", opts.Interval, "duration", opts.Duration, "out", path, "throttling", throttlePath)

	if err := bench.SampleTo(ctx, run, opts.Interval, path, throttlePath, vip); err != nil {
		return fmt.Errorf("sampling: %w", err)
	}

	slog.Info("Wrote series", "path", path, "throttling", throttlePath)

	return nil
}

// BenchHealthOpts configures `hhfab vlab bench health`.
type BenchHealthOpts struct {
	Stats bool
	QPS   float32
	Burst int
}

// DoVLABBenchHealth prints the current state of the control plane.
func DoVLABBenchHealth(ctx context.Context, workDir, cacheDir string, opts BenchHealthOpts) error {
	// Without stats nothing reads the API, so do not even open a client.
	var kube kclient.Client
	if opts.Stats {
		var err error
		kube, err = bench.NewKubeClient(ctx, filepath.Join(workDir, VLABDir, VLABKubeConfig), opts.QPS, opts.Burst)
		if err != nil {
			return err //nolint:wrapcheck
		}
	}

	// etcd metrics are bound to localhost on the control node and the node
	// stats obviously are too, so those sections need a shell there. Losing it
	// degrades the report rather than failing it.
	run, err := controlNodeRunner(ctx, workDir, cacheDir)
	if err != nil {
		slog.Warn("No control node access, skipping etcd and node sections", "err", err)
	}

	if err := bench.Health(ctx, kube, run, os.Stdout, bench.HealthOpts{
		Stats:    opts.Stats,
		Series:   filepath.Join(workDir, bench.SeriesFile),
		Throttle: filepath.Join(workDir, bench.ThrottleFile),
	}); err != nil {
		return fmt.Errorf("collecting health: %w", err)
	}

	return nil
}

// controlNodeRunner returns a command runner for the VLAB control node.
func controlNodeRunner(ctx context.Context, workDir, cacheDir string) (bench.Runner, error) {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return nil, err
	}

	for _, vm := range vlab.VMs {
		if vm.Type != VMTypeControl {
			continue
		}

		ssh, err := c.SSHVM(ctx, vlab, vm)
		if err != nil {
			return nil, fmt.Errorf("preparing ssh to %s: %w", vm.Name, err)
		}

		return func(ctx context.Context, cmd string) (string, error) {
			stdout, stderr, err := ssh.Run(ctx, cmd)
			if err != nil {
				return "", fmt.Errorf("running %q on %s: %w: %s", cmd, vm.Name, err, strings.TrimSpace(stderr))
			}

			return stdout, nil
		}, nil
	}

	return nil, fmt.Errorf("no control node in the VLAB") //nolint:err113
}

// BenchCleanOpts configures `hhfab vlab bench clean`.
type BenchCleanOpts struct {
	Fabrics []string
	QPS     float32
	Burst   int
}

// DoVLABBenchClean removes everything the benchmark created.
func DoVLABBenchClean(ctx context.Context, workDir, cacheDir string, opts BenchCleanOpts) error {
	if _, err := benchLoad(ctx, workDir, cacheDir); err != nil {
		return err
	}

	kube, err := bench.NewKubeClient(ctx, filepath.Join(workDir, VLABDir, VLABKubeConfig), opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	start := time.Now()

	results, err := bench.Clean(ctx, kube, opts.Fabrics)

	total := 0
	for _, res := range results {
		total += res.Deleted
	}

	if err != nil {
		return fmt.Errorf("cleaning: %w", err)
	}

	slog.Info("Benchmark objects removed", "count", total, "took", time.Since(start).Truncate(time.Millisecond))

	return nil
}

// benchGenerate builds the topology into an in-memory loader and validates it
// with exactly the Default() plus Validate() the admission webhooks run, so a
// bad shape fails before anything reaches the cluster.
func benchGenerate(ctx context.Context, c *Config, specs []bench.FabricSpec, skipValidate, defaultFabric bool) (*bench.Generator, *apiutil.Loader, error) {
	gateways := uint(0)
	for _, node := range c.Nodes {
		if slices.Contains(node.Spec.Roles, fabapi.NodeRoleGateway) {
			gateways++
		}
	}

	alloc, err := bench.NewAllocator(c.Fab, uint(len(c.Controls)), uint(len(c.Nodes)), gateways, specs)
	if err != nil {
		return nil, nil, fmt.Errorf("planning addresses: %w", err)
	}

	fabricCfg, err := fabcomp.GetFabricConfig(c.Fab)
	if err != nil {
		return nil, nil, fmt.Errorf("getting fabric config: %w", err)
	}
	if fabricCfg, err = fabricCfg.Init(fmeta.ExtraValidators{Peering: valid.Peering}); err != nil {
		return nil, nil, fmt.Errorf("initializing fabric config: %w", err)
	}

	l := apiutil.NewLoader()

	profiles := switchprofile.NewDefaultSwitchProfiles()
	if err := profiles.RegisterAll(ctx, l.GetClient(), fabricCfg); err != nil {
		return nil, nil, fmt.Errorf("registering switch profiles: %w", err)
	}

	byName := map[string]*wiringapi.SwitchProfile{}
	for _, sp := range profiles.List() {
		byName[sp.Name] = sp
	}

	gen, err := bench.NewGenerator(specs, alloc, byName)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing generator: %w", err)
	}

	if defaultFabric {
		if err := gen.InDefaultFabric(); err != nil {
			return nil, nil, fmt.Errorf("placing switches in the default fabric: %w", err)
		}
	}

	if err := gen.Generate(ctx, l); err != nil {
		return nil, nil, fmt.Errorf("generating: %w", err)
	}

	if skipValidate {
		slog.Warn("Skipping local validation, relying on the admission webhooks")

		return gen, l, nil
	}

	// Local validation runs the fabric API this binary was built with, while a
	// default fabric topology is meant for a release from before Fabric objects,
	// with its own rules - and current validation requires the Fabric/default
	// that only a running controller creates.
	if defaultFabric {
		slog.Warn("Skipping local validation for the default fabric, relying on the target release's admission webhooks")

		return gen, l, nil
	}

	// Validation is quadratic for the same reason the apply is - Connection
	// validation lists every other connection - but against the in-memory
	// client it is slower than the cluster: a full 96 switch fabric takes ~50s
	// locally against ~20s to apply. Worth it to fail before touching the
	// cluster at normal sizes, worth skipping on a big run, which is what
	// --skip-validate is for. The admission webhooks validate either way.
	start := time.Now()

	if err := apiutil.ValidateFabricGateway(ctx, l, fabricCfg); err != nil {
		return nil, nil, fmt.Errorf("validating generated topology: %w", err)
	}

	slog.Info("Validated generated topology", "took", time.Since(start).Truncate(time.Millisecond))

	return gen, l, nil
}

// benchDryRun writes the generated objects as YAML instead of applying them.
func benchDryRun(ctx context.Context, l *apiutil.Loader, out string) error {
	if out == "" {
		if err := apiutil.PrintInclude(ctx, l.GetClient(), os.Stdout); err != nil {
			return fmt.Errorf("writing objects: %w", err)
		}

		return nil
	}

	f, err := os.OpenFile(out, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating %q: %w", out, err)
	}

	writeErr := apiutil.PrintInclude(ctx, l.GetClient(), f)

	// Close is checked rather than deferred: it is where a buffered write that
	// failed finally reports it, and ignoring it would claim a file was written
	// that is incomplete.
	if err := f.Close(); err != nil && writeErr == nil {
		writeErr = fmt.Errorf("closing %q: %w", out, err)
	}

	if writeErr != nil {
		return fmt.Errorf("writing objects: %w", writeErr)
	}

	slog.Info("Wrote generated topology", "path", out)

	return nil
}

// checkCoexistence refuses to run against a cluster that already has switches
// the bench did not create: the allocator derives its bases from what the
// control plane owns, so real switches would overlap it.
func checkCoexistence(ctx context.Context, kube kclient.Client, force bool) error {
	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}

	foreign := []string{}
	for _, sw := range switches.Items {
		if _, ok := sw.Labels[bench.LabelFabric]; !ok {
			foreign = append(foreign, sw.Name)
		}
	}

	if len(foreign) == 0 {
		return nil
	}

	// Counted before the list is cut down for display, and counting only the
	// foreign switches: on a re-run the bench's own switches are there too.
	count := len(foreign)

	sort.Strings(foreign)
	if len(foreign) > 5 {
		foreign = append(foreign[:5], "...")
	}

	if !force {
		return fmt.Errorf("cluster already has %d switch(es) the bench did not create (%s); the address allocator assumes it owns the pools, so use a switchless VLAB (hhfab vlab gen --no-switches) or pass --force to proceed anyway", //nolint:err113
			count, strings.Join(foreign, ", "))
	}

	slog.Warn("Proceeding with non-bench switches present", "count", count, "switches", strings.Join(foreign, ", "))

	return nil
}

// summarize prints what is about to be created, per the plan: counts, then go.
func summarize(specs []bench.FabricSpec, gen *bench.Generator) {
	for _, spec := range specs {
		slog.Info("Fabric", "name", spec.Name, "domains", spec.Domains,
			"spines", spec.Spines, "leaves", spec.Leaves,
			"serverPorts", spec.ServerPorts, "breakout", spec.ServerBreakout,
			"servers", spec.Servers(), "sysnameOverride", fmt.Sprintf("%d%%", spec.SysNameOverride),
			"vpcs", spec.VPCs, "attach", spec.Attach, "peerings", spec.Peerings)
	}

	objects := gen.Objects()

	kinds := make([]string, 0, len(objects))
	for kind := range objects {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	total := uint(0)
	for _, kind := range kinds {
		slog.Info("Objects", "kind", kind, "count", objects[kind])
		total += objects[kind]
	}

	slog.Info("Objects total", "count", total,
		"note", "the Fabric controller will add an Agent and 5 RBAC/Secret objects per switch")
}

func reportPhases(results []bench.PhaseResult) {
	if len(results) == 0 {
		return
	}

	total := time.Duration(0)
	for _, res := range results {
		rate := float64(res.Objects) / res.Took.Seconds()
		slog.Info("Phase", "name", res.Phase, "objects", res.Objects,
			"created", res.Created, "updated", res.Updated,
			"took", res.Took.Truncate(time.Millisecond), "rate", fmt.Sprintf("%.1f/s", rate))
		total += res.Took
	}

	slog.Info("Phases total", "took", total.Truncate(time.Millisecond))
}
