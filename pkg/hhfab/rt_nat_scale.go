// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/support"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	corev1 "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// MASQUERADE_CLOSED_HOLD in nat/src/masquerade/nf.rs at dataplane v0.38.4: a TCP mapping is kept this long after
	// its last flow is gone, whatever the idle timeout
	natScaleHold = 4 * time.Minute
	// the pool has to be full this long after the first fill connection, the ports of the first ones are released then
	natScaleFillTimeout = natScaleHold
	// counted from the last fill connection closing: the hold, up to about 82 s more for the expiry timer wheel, and a
	// margin
	natScaleReleaseTimeout = natScaleHold + 3*time.Minute
	// the pool is one public IP
	natScalePoolPorts = masqPortsPerIP
	// below this rate the ports of the first connections are released before the pool is full, rounded up
	natScaleMinPortsPerSec = (natScalePoolPorts + int(natScaleHold/time.Second) - 1) / int(natScaleHold/time.Second)
	natScaleRateWindow     = time.Minute
	// three fill source IPs fill the pool with one connection per source port, the fourth is a margin
	natScalePortsPerSource  = natScalePoolPorts / 3
	natScaleSourceIPs       = 4
	natScaleFirstSourcePort = 10000
	natScaleParallel        = 64
	natScaleConnectTimeout  = 3 * time.Second
	natScaleIdleTimeout     = 30 * time.Minute
	natScalePollInterval    = 5 * time.Second
	natScaleReadTimeout     = 10 * time.Second
	natScaleStopTimeout     = 30 * time.Second
	natScaleRevertTimeout   = 2 * time.Minute
	natScalePoolCIDR        = "192.168.200.1/32"
	natScalePort            = 8080
	natScaleUnitPrefix      = "hhfab-nat-scale-"
	natScaleListenerUnit    = natScaleUnitPrefix + "listener"
)

var (
	errNATScaleVirtualSwitch  = errors.New("the servers of a VLAB open connections too slowly to fill the pool")
	errNATScaleNoServers      = errors.New("no server to use in one of the VPCs")
	errNATScaleNoFreeIPs      = errors.New("not enough free IPs in the subnet")
	errNATScaleNoInterface    = errors.New("no interface with the server IP")
	errNATScaleNoSubnet       = errors.New("no such subnet in the VPC")
	errNATScaleSlowFill       = errors.New("the fill allocated new ports too slowly")
	errNATScaleFillTimeout    = errors.New("the pool was not full in time")
	errNATScaleSourcesOut     = errors.New("the source IPs ran out before the pool was full")
	errNATScaleRelease        = errors.New("the held ports were not released in time")
	errNATScaleFinalConn      = errors.New("the connection from a source IP never used before failed")
	errNATScaleGatewayRestart = errors.New("a gateway pod was replaced or restarted")
	errNATScaleUnitOutput     = errors.New("unexpected output of the unit state")
)

// readMasqueradePool reads the TCP masquerade pool of the VPC pair on every gateway pod, the held ports are summed and
// the pool is full if it is full on any pod. A pod without the pool adds nothing, no pod with it is
// errMasqPoolNotFound. The output that could not be parsed is returned with the error.
func readMasqueradePool(ctx context.Context, clientset *kubernetes.Clientset, cfg *rest.Config, pods []corev1.Pod, srcVNI, dstVNI uint32) (masqPoolState, string, error) {
	total := masqPoolState{}
	found := 0
	for idx := range pods {
		pod := &pods[idx]
		if pod.DeletionTimestamp != nil {
			continue
		}

		readCtx, cancel := context.WithTimeout(ctx, natScaleReadTimeout)
		out, stderr, err := support.ExecPodContainerCommandOnce(readCtx, clientset, cfg, comp.FabNamespace, pod.Name, support.GatewayDataplaneContainer,
			[]string{"/dataplane-cli", "-c", "show masquerading state"})
		cancel()
		if err != nil {
			return masqPoolState{}, "", fmt.Errorf("reading the masquerading state of pod %s: %w: %s", pod.Name, err, stderr)
		}

		pool, err := parseMasqueradeState(out, srcVNI, dstVNI)
		if errors.Is(err, errMasqPoolNotFound) {
			continue
		}
		if err != nil {
			return masqPoolState{}, out, fmt.Errorf("pod %s: %w", pod.Name, err)
		}
		found++
		total.HeldPorts += pool.HeldPorts
		total.Ranges += pool.Ranges
		total.Full = total.Full || pool.Full
	}
	if found == 0 {
		return masqPoolState{}, "", fmt.Errorf("%w on any gateway pod", errMasqPoolNotFound)
	}

	return total, "", nil
}

// startLoad opens one TCP connection from each source port of the range, natScaleParallel at a time, in a transient
// systemd unit that stays loaded after it exits, so that loadDone can read when.
func startLoad(ctx context.Context, ssh *sshutil.Config, unit string, src netip.Addr, dst netip.AddrPort, firstPort, lastPort uint16) error {
	cmd := fmt.Sprintf("sudo systemd-run --unit=%s --property=RemainAfterExit=yes sh -c 'seq %d %d | xargs -P %d -I{} socat -u /dev/null TCP:%s,bind=%s:{},connect-timeout=%d'",
		unit, firstPort, lastPort, natScaleParallel, dst, src, int(natScaleConnectTimeout/time.Second))
	if _, stderr, err := ssh.Run(ctx, cmd); err != nil {
		return fmt.Errorf("starting unit %s: %w: %s", unit, err, stderr)
	}

	return nil
}

// parseLoadDone parses the server time and the exit time of a unit's main process, one per line, and returns how long
// ago the process exited. A missing or empty exit time is a process still running.
func parseLoadDone(out string) (time.Duration, bool, error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > 2 {
		return 0, false, fmt.Errorf("%w: %d lines, 1 or 2 expected: %q", errNATScaleUnitOutput, len(lines), out)
	}
	now, err := strconv.ParseInt(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: server time %q: %w", errNATScaleUnitOutput, lines[0], err)
	}
	if len(lines) == 1 || strings.TrimSpace(lines[1]) == "" {
		return 0, false, nil
	}
	exitedAt, ok := strings.CutPrefix(strings.TrimSpace(lines[1]), "@")
	if !ok {
		return 0, false, fmt.Errorf("%w: exit time %q", errNATScaleUnitOutput, lines[1])
	}
	exited, err := strconv.ParseInt(exitedAt, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: exit time %q: %w", errNATScaleUnitOutput, lines[1], err)
	}

	return time.Duration(now-exited) * time.Second, true, nil
}

// loadDone tells whether the main process of the unit exited and when, by the server's clock turned into the local one.
func loadDone(ctx context.Context, ssh *sshutil.Config, unit string) (time.Time, bool, error) {
	stdout, stderr, err := ssh.Run(ctx, fmt.Sprintf("date +%%s; systemctl show %s --timestamp=unix -p ExecMainExitTimestamp --value", unit))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading unit %s: %w: %s", unit, err, stderr)
	}
	ago, done, err := parseLoadDone(stdout)
	if err != nil || !done {
		return time.Time{}, false, err
	}

	return time.Now().Add(-ago), true, nil
}

// containerRestarts is the restart count of every container of the pods, by pod UID and container name.
func containerRestarts(pods []corev1.Pod) map[ktypes.UID]map[string]int32 {
	restarts := make(map[ktypes.UID]map[string]int32, len(pods))
	for idx := range pods {
		pod := &pods[idx]
		counts := map[string]int32{}
		for _, status := range pod.Status.ContainerStatuses {
			counts[status.Name] = status.RestartCount
		}
		restarts[pod.UID] = counts
	}

	return restarts
}

// gatewayChange returns an error if a pod was added or removed since the start or one of its containers restarted.
func gatewayChange(start, pods []corev1.Pod) error {
	startRestarts := containerRestarts(start)
	current := make(map[ktypes.UID]bool, len(pods))
	for idx := range pods {
		pod := &pods[idx]
		current[pod.UID] = true
		counts, ok := startRestarts[pod.UID]
		if !ok {
			return fmt.Errorf("%w: pod %s is new", errNATScaleGatewayRestart, pod.Name)
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.RestartCount <= counts[status.Name] {
				continue
			}
			term := status.LastTerminationState.Terminated
			if term == nil {
				term = &corev1.ContainerStateTerminated{}
			}

			return fmt.Errorf("%w: container %s of pod %s restarted %d times during the test, last exit %s code %d", errNATScaleGatewayRestart,
				status.Name, pod.Name, status.RestartCount-counts[status.Name], term.Reason, term.ExitCode)
		}
	}
	for idx := range start {
		if !current[start[idx].UID] {
			return fmt.Errorf("%w: pod %s is gone", errNATScaleGatewayRestart, start[idx].Name)
		}
	}

	return nil
}

type natScaleFillOutcome int

const (
	natScaleFillContinue natScaleFillOutcome = iota
	natScaleFillFull
	natScaleFillSlow
	natScaleFillSourcesOut
	natScaleFillTimedOut
)

// natScaleFillRate is the new ports per second since the first fill connection.
func natScaleFillRate(pool masqPoolState, preFill int, elapsed time.Duration) float64 {
	return float64(pool.HeldPorts-preFill) / elapsed.Seconds()
}

// natScaleFillDecision decides a fill round from the state read. The rate is checked once, at the first round after
// natScaleRateWindow. loadDone is whether the unit of the last source IP started had exited before the state was read,
// so the ports of its last connections are in the state.
func natScaleFillDecision(pool masqPoolState, preFill int, elapsed time.Duration, sourcesUsed int, loadDone, rateChecked bool) natScaleFillOutcome {
	switch {
	case pool.Full:
		return natScaleFillFull
	case elapsed >= natScaleFillTimeout:
		return natScaleFillTimedOut
	case !rateChecked && elapsed >= natScaleRateWindow && natScaleFillRate(pool, preFill, elapsed) < float64(natScaleMinPortsPerSec):
		return natScaleFillSlow
	case loadDone && sourcesUsed >= natScaleSourceIPs:
		return natScaleFillSourcesOut
	default:
		return natScaleFillContinue
	}
}

type natScaleReleaseOutcome int

const (
	natScaleReleaseWait natScaleReleaseOutcome = iota
	natScaleReleased
	natScaleReleaseTimedOut
)

// natScaleReleaseDecision decides a release round from the state read and the time since the last fill connection
// closed.
func natScaleReleaseDecision(pool masqPoolState, preFill int, sinceLastClosed time.Duration) natScaleReleaseOutcome {
	switch {
	case pool.ReleasedTo(preFill):
		return natScaleReleased
	case sinceLastClosed >= natScaleReleaseTimeout:
		return natScaleReleaseTimedOut
	default:
		return natScaleReleaseWait
	}
}

// natScaleRun is the state of one run of gatewayNATPortExhaustionTest.
type natScaleRun struct {
	testCtx   *VPCPeeringTestCtx
	clientset *kubernetes.Clientset
	restCfg   *rest.Config

	startPods []corev1.Pod
	vnis      [2]uint32

	src, dst       *ServerEndpoint
	srcSSH, dstSSH *sshutil.Config
	dstAddr        netip.AddrPort
	iface          string
	prefix         netip.Prefix
	fill           []netip.Addr
	neverUsed      netip.Addr

	preFill     masqPoolState
	pool        masqPoolState
	fillStart   time.Time
	lastClosed  time.Time
	sourcesUsed int
	loadDone    bool
	rateChecked bool
}

func newNATScaleRun(ctx context.Context, testCtx *VPCPeeringTestCtx) (*natScaleRun, error) {
	kubeconfig := filepath.Join(testCtx.vlabCfg.WorkDir, VLABDir, VLABKubeConfig)
	clientset, err := kubeutil.NewClientset(ctx, kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	restCfg, err := kubeutil.NewClientConfig(ctx, kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client config: %w", err)
	}

	return &natScaleRun{testCtx: testCtx, clientset: clientset, restCfg: restCfg}, nil
}

func (r *natScaleRun) listPods(ctx context.Context) ([]corev1.Pod, error) {
	gws := &gwapi.GatewayList{}
	if err := r.testCtx.kube.List(ctx, gws); err != nil {
		return nil, fmt.Errorf("listing gateways: %w", err)
	}
	pods := []corev1.Pod{}
	for _, gw := range gws.Items {
		gwPods, err := listGatewayPods(ctx, r.testCtx.kube, gw.Name)
		if err != nil {
			return nil, err
		}
		pods = append(pods, gwPods...)
	}

	return pods, nil
}

// snapshotGateways records the gateway pods, checkGateways compares with them.
func (r *natScaleRun) snapshotGateways(ctx context.Context) error {
	pods, err := r.listPods(ctx)
	if err != nil {
		return err
	}
	r.startPods = pods

	return nil
}

func (r *natScaleRun) checkGateways(ctx context.Context) error {
	pods, err := r.listPods(ctx)
	if err != nil {
		return err
	}

	return gatewayChange(r.startPods, pods)
}

func natScaleSpec() natTestSpec {
	return natTestSpec{
		Name: "gateway NAT port exhaustion",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{
				VPC1NATCIDR:    []string{natScalePoolCIDR},
				VPC1NATMode:    NATModeMasquerade,
				NATIdleTimeout: natScaleIdleTimeout,
			})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			overrideVPCToVPCVerdict(matrix, vpc1.Name, vpc2.Name, VerdictAllow)
			overrideVPCToVPCVerdict(matrix, vpc2.Name, vpc1.Name, VerdictDeny)

			return nil
		},
	}
}

func (r *natScaleRun) readVNIs(ctx context.Context, vpc1, vpc2 *vpcapi.VPC) error {
	for idx, vpc := range []*vpcapi.VPC{vpc1, vpc2} {
		info := &gwapi.VPCInfo{}
		if err := r.testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: vpc.Name}, info); err != nil {
			return fmt.Errorf("getting VPCInfo %s: %w", vpc.Name, err)
		}
		r.vnis[idx] = info.Spec.VNI
	}

	return nil
}

func (r *natScaleRun) readPool(ctx context.Context) (masqPoolState, string, error) {
	return readMasqueradePool(ctx, r.clientset, r.restCfg, r.startPods, r.vnis[0], r.vnis[1])
}

func (r *natScaleRun) unparsableRead(err error, raw string) error {
	slog.Info("Output that could not be parsed:\n" + raw)

	return fmt.Errorf("reading the masquerade pool: %w", err)
}

// pollPool reads the pool into r.pool for a poll round: false without an error is a read to retry.
func (r *natScaleRun) pollPool(ctx context.Context) (bool, error) {
	read, raw, err := r.readPool(ctx)
	switch {
	case errors.Is(err, errMasqStateMalformed):
		return false, r.unparsableRead(err, raw)
	case errors.Is(err, errMasqPoolNotFound):
		return false, err
	case err != nil:
		slog.Warn("Reading the masquerade pool", "err", err)

		return false, nil
	}
	r.pool = read

	return true, nil
}

// cleanup is the one revert of the test. Every step runs whatever happened to the ones before it, and each tolerates
// the thing it removes not being there.
func (r *natScaleRun) cleanup(peering *gwapi.GatewayPeering) RevertFunc {
	return func(ctx context.Context) error {
		ctx = context.WithoutCancel(ctx)

		return errors.Join(r.stopLoadUnits(ctx), r.stopListener(ctx), r.removeSourceIPs(ctx), r.deletePeering(ctx, peering))
	}
}

func (r *natScaleRun) stopLoadUnits(ctx context.Context) error {
	if r.srcSSH == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, natScaleRevertTimeout)
	defer cancel()

	if _, stderr, err := r.srcSSH.Run(ctx, fmt.Sprintf("sudo systemctl stop '%sload-*'; sudo systemctl reset-failed '%sload-*' 2>/dev/null; true", natScaleUnitPrefix, natScaleUnitPrefix)); err != nil {
		return fmt.Errorf("stopping the load generator on %s: %w: %s", r.src.Name, err, stderr)
	}

	return nil
}

func (r *natScaleRun) stopListener(ctx context.Context) error {
	if r.dstSSH == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, natScaleRevertTimeout)
	defer cancel()

	if _, stderr, err := r.dstSSH.Run(ctx, fmt.Sprintf("sudo systemctl stop %[1]s 2>/dev/null; ! systemctl is-active --quiet %[1]s", natScaleListenerUnit)); err != nil {
		return fmt.Errorf("stopping the listener on %s: %w: %s", r.dst.Name, err, stderr)
	}

	return nil
}

func (r *natScaleRun) removeSourceIPs(ctx context.Context) error {
	if r.srcSSH == nil || len(r.fill) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, natScaleRevertTimeout)
	defer cancel()

	addrs := append(slices.Clone(r.fill), r.neverUsed)
	del := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		del = append(del, fmt.Sprintf("if ip -o addr show dev %[2]s | grep -qw %[1]s; then sudo ip addr del %[1]s/%[3]d dev %[2]s; fi", addr, r.iface, r.prefix.Bits()))
	}
	if _, stderr, err := r.srcSSH.Run(ctx, strings.Join(del, " && ")); err != nil {
		return fmt.Errorf("removing the source IPs from %s: %w: %s", r.src.Name, err, stderr)
	}

	return nil
}

// deletePeering deletes the peering and waits for the pool and its ports to be gone.
func (r *natScaleRun) deletePeering(ctx context.Context, peering *gwapi.GatewayPeering) error {
	ctx, cancel := context.WithTimeout(ctx, natScaleRevertTimeout)
	defer cancel()

	if err := kclient.IgnoreNotFound(r.testCtx.kube.Delete(ctx, peering)); err != nil {
		return fmt.Errorf("deleting GatewayPeering %s: %w", peering.Name, err)
	}

	var last masqPoolState
	var lastErr error
	if err := wait.PollUntilContextTimeout(ctx, natScalePollInterval, natScaleRevertTimeout, true, func(ctx context.Context) (bool, error) {
		last, _, lastErr = r.readPool(ctx)
		if errors.Is(lastErr, errMasqPoolNotFound) {
			return true, nil
		}

		return lastErr == nil && last.ReleasedTo(0), nil
	}); err != nil {
		return errors.Join(fmt.Errorf("ports held after deleting GatewayPeering %s: held %d, full %t: %w", peering.Name, last.HeldPorts, last.Full, err), lastErr)
	}

	return nil
}

// pickServers picks the source server in vpc1 and the destination in vpc2, and returns the IPs of all servers.
func (r *natScaleRun) pickServers(matrix *ConnectivityMatrix, vpc1, vpc2 *vpcapi.VPC) (map[netip.Addr]bool, error) {
	used := map[netip.Addr]bool{}
	for _, ep := range matrix.AllEndpoints {
		if ep.Server == nil {
			continue
		}
		used[ep.Server.IP.Unmap()] = true
		switch {
		case ep.Server.HostBGP:
		case r.src == nil && ep.Server.VPC == vpc1.Name:
			r.src = ep.Server
		case r.dst == nil && ep.Server.VPC == vpc2.Name:
			r.dst = ep.Server
		}
	}
	if r.src == nil || r.dst == nil {
		return nil, fmt.Errorf("%w: %s or %s", errNATScaleNoServers, vpc1.Name, vpc2.Name)
	}
	r.dstAddr = netip.AddrPortFrom(r.dst.IP, natScalePort)

	return used, nil
}

// connectServers gets the ssh configs of both servers and the interface of the source server's IP.
func (r *natScaleRun) connectServers(ctx context.Context) error {
	var err error
	if r.srcSSH, err = r.testCtx.getSSH(ctx, r.src.Name); err != nil {
		return fmt.Errorf("getting ssh config for %s: %w", r.src.Name, err)
	}
	if r.dstSSH, err = r.testCtx.getSSH(ctx, r.dst.Name); err != nil {
		return fmt.Errorf("getting ssh config for %s: %w", r.dst.Name, err)
	}

	serverIPs, err := discoverServerIPs(ctx, r.srcSSH, r.src.Name)
	if err != nil {
		return err
	}
	for _, sip := range serverIPs {
		if sip.prefix.Addr() == r.src.IP {
			r.iface, r.prefix = sip.iface, sip.prefix
		}
	}
	if r.iface == "" {
		return fmt.Errorf("%w: %s on %s", errNATScaleNoInterface, r.src.IP, r.src.Name)
	}

	return nil
}

// pickSourceIPs picks the fill source IPs and the one never used from the top of the subnet, skipping used, the
// gateway and the DHCP leases.
func (r *natScaleRun) pickSourceIPs(ctx context.Context, vpc1 *vpcapi.VPC, used map[netip.Addr]bool) error {
	subnet := vpc1.Spec.Subnets[r.src.Subnet]
	if subnet == nil {
		return fmt.Errorf("%w: %s in %s", errNATScaleNoSubnet, r.src.Subnet, vpc1.Name)
	}
	if gateway, err := netip.ParseAddr(subnet.Gateway); err == nil {
		used[gateway] = true
	}
	dhcpSubnet := &dhcpapi.DHCPSubnet{}
	dhcpSubnetName := vpc1.Name + "--" + r.src.Subnet
	if err := r.testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: dhcpSubnetName}, dhcpSubnet); err != nil {
		return fmt.Errorf("getting DHCPSubnet %s: %w", dhcpSubnetName, err)
	}
	for _, lease := range dhcpSubnet.Status.Allocated {
		if addr, err := netip.ParseAddr(lease.IP); err == nil {
			used[addr] = true
		}
	}

	addrs := make([]netip.Addr, 0, natScaleSourceIPs+1)
	for len(addrs) < natScaleSourceIPs+1 {
		addr, err := pickUnusedHostAddress(r.prefix, used, true)
		if err != nil {
			return fmt.Errorf("%w: %d of %d picked in %s: %w", errNATScaleNoFreeIPs, len(addrs), natScaleSourceIPs+1, r.prefix.Masked(), err)
		}
		used[addr] = true
		addrs = append(addrs, addr)
	}
	r.fill, r.neverUsed = addrs[:natScaleSourceIPs], addrs[natScaleSourceIPs]

	return nil
}

// addSourceIPs adds the fill source IPs and the one never used on the source server.
func (r *natScaleRun) addSourceIPs(ctx context.Context) error {
	addrs := append(slices.Clone(r.fill), r.neverUsed)
	add := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		add = append(add, fmt.Sprintf("sudo ip addr add %s/%d dev %s", addr, r.prefix.Bits(), r.iface))
	}
	if _, stderr, err := r.srcSSH.Run(ctx, strings.Join(add, " && ")); err != nil {
		return fmt.Errorf("adding the source IPs on %s: %w: %s", r.src.Name, err, stderr)
	}

	return nil
}

func (r *natScaleRun) startListener(ctx context.Context) error {
	if _, stderr, err := r.dstSSH.Run(ctx, fmt.Sprintf("sudo systemd-run --unit=%s socat TCP-LISTEN:%d,reuseaddr,fork,backlog=4096 EXEC:/bin/true", natScaleListenerUnit, natScalePort)); err != nil {
		return fmt.Errorf("starting the listener on %s: %w: %s", r.dst.Name, err, stderr)
	}

	return nil
}

func (r *natScaleRun) readPreFill(ctx context.Context) error {
	preFill, raw, err := r.readPool(ctx)
	switch {
	case errors.Is(err, errMasqStateMalformed):
		return r.unparsableRead(err, raw)
	case err != nil:
		return err
	}
	r.preFill = preFill
	slog.Info(fmt.Sprintf("Held ports before the fill: %d", preFill.HeldPorts))

	return nil
}

// unit is the load unit of the last source IP started.
func (r *natScaleRun) unit() string {
	return fmt.Sprintf("%sload-%d", natScaleUnitPrefix, r.sourcesUsed-1)
}

// startNextLoad starts the load of the next source IP.
func (r *natScaleRun) startNextLoad(ctx context.Context) error {
	r.sourcesUsed++

	return startLoad(ctx, r.srcSSH, r.unit(), r.fill[r.sourcesUsed-1], r.dstAddr, natScaleFirstSourcePort, natScaleFirstSourcePort+natScalePortsPerSource-1)
}

// startFill starts the load of the first source IP.
func (r *natScaleRun) startFill(ctx context.Context) error {
	r.fillStart = time.Now()

	return r.startNextLoad(ctx)
}

func (r *natScaleRun) fillProgress() string {
	return fmt.Sprintf("held %d of %d ports, %d source IPs used, %s after the first fill connection",
		r.pool.HeldPorts, natScalePoolPorts, r.sourcesUsed, time.Since(r.fillStart).Round(time.Second))
}

// stopLoad stops the load unit once the pool is full, its exit is the last fill connection closing. A unit that
// already exited, also a failed one, is not killed.
func (r *natScaleRun) stopLoad(ctx context.Context) error {
	exited, done, err := loadDone(ctx, r.srcSSH, r.unit())
	if err != nil {
		slog.Warn("Reading the load unit", "unit", r.unit(), "err", err)
	}
	if err == nil && done {
		r.lastClosed = exited

		return nil
	}
	if _, stderr, err := r.srcSSH.Run(ctx, "sudo systemctl kill "+r.unit()); err != nil {
		slog.Warn("Stopping the load unit", "unit", r.unit(), "err", err, "stderr", stderr)
	}
	if err := wait.PollUntilContextTimeout(ctx, time.Second, natScaleStopTimeout, true, func(ctx context.Context) (bool, error) {
		exited, done, err := loadDone(ctx, r.srcSSH, r.unit())
		if err != nil {
			slog.Warn("Reading the load unit", "unit", r.unit(), "err", err)

			return false, nil
		}
		r.lastClosed = exited

		return done, nil
	}); err != nil {
		return fmt.Errorf("waiting for unit %s to stop: %w", r.unit(), err)
	}

	return nil
}

// advanceLoad starts the next source IP once the running load unit exited, or records that the last one did.
func (r *natScaleRun) advanceLoad(ctx context.Context) error {
	exited, done, err := loadDone(ctx, r.srcSSH, r.unit())
	if err != nil {
		slog.Warn("Reading the load unit", "unit", r.unit(), "err", err)

		return nil
	}
	if !done {
		return nil
	}
	r.lastClosed = exited
	if r.sourcesUsed == natScaleSourceIPs {
		r.loadDone = true

		return nil
	}

	return r.startNextLoad(ctx)
}

func (r *natScaleRun) fillRound(ctx context.Context) (bool, error) {
	if ok, err := r.pollPool(ctx); !ok || err != nil {
		return false, err
	}

	elapsed := time.Since(r.fillStart)
	outcome := natScaleFillDecision(r.pool, r.preFill.HeldPorts, elapsed, r.sourcesUsed, r.loadDone, r.rateChecked)
	if !r.rateChecked && elapsed >= natScaleRateWindow && outcome != natScaleFillFull && outcome != natScaleFillTimedOut {
		r.rateChecked = true
		slog.Info(fmt.Sprintf("Fill rate in the first %s: %.0f new ports per second", elapsed.Round(time.Second), natScaleFillRate(r.pool, r.preFill.HeldPorts, elapsed)))
	}

	switch outcome {
	case natScaleFillFull:
		return true, r.stopLoad(ctx)
	case natScaleFillSlow:
		return false, fmt.Errorf("%w: %.0f new ports per second, %d needed, %s", errNATScaleSlowFill,
			natScaleFillRate(r.pool, r.preFill.HeldPorts, elapsed), natScaleMinPortsPerSec, r.fillProgress())
	case natScaleFillSourcesOut:
		return false, fmt.Errorf("%w: %s", errNATScaleSourcesOut, r.fillProgress())
	case natScaleFillTimedOut:
		return false, fmt.Errorf("%w: %s", errNATScaleFillTimeout, r.fillProgress())
	case natScaleFillContinue:
	}

	return false, r.advanceLoad(ctx)
}

// fillPool polls until the pool is full, the poll timeout leaves a round past natScaleFillTimeout to the decision.
func (r *natScaleRun) fillPool(ctx context.Context) error {
	err := wait.PollUntilContextTimeout(ctx, natScalePollInterval, natScaleFillTimeout+natScalePollInterval, false, r.fillRound)
	slog.Info(fmt.Sprintf("Fill: %s, %d port ranges in the last state read", r.fillProgress(), r.pool.Ranges))
	if err != nil {
		if gwErr := r.checkGateways(ctx); gwErr != nil {
			return gwErr
		}
		if wait.Interrupted(err) {
			return fmt.Errorf("%w: %s", errNATScaleFillTimeout, r.fillProgress())
		}

		return fmt.Errorf("filling the pool: %w", err)
	}
	slog.Info("Masquerade pool full", "sources", r.sourcesUsed, "took", time.Since(r.fillStart).Round(time.Second))

	return nil
}

func (r *natScaleRun) tryNeverUsed(ctx context.Context) error {
	cmd := fmt.Sprintf("socat -u /dev/null TCP:%s,bind=%s,connect-timeout=%d", r.dstAddr, r.neverUsed, int(natScaleConnectTimeout/time.Second))
	if _, stderr, err := r.srcSSH.Run(ctx, cmd); err != nil {
		return fmt.Errorf("%w: from %s: %w: %s", errNATScaleFinalConn, r.neverUsed, err, strings.TrimSpace(stderr))
	}

	return nil
}

func (r *natScaleRun) releaseProgress() string {
	return fmt.Sprintf("held %d of %d ports, %d before the fill, %d source IPs used, %s after the last fill connection closed",
		r.pool.HeldPorts, natScalePoolPorts, r.preFill.HeldPorts, r.sourcesUsed, time.Since(r.lastClosed).Round(time.Second))
}

// waitRelease polls until the held ports are back to the pre-fill level, the poll timeout leaves a round past
// natScaleReleaseTimeout to the decision.
func (r *natScaleRun) waitRelease(ctx context.Context) error {
	timeout := max(time.Until(r.lastClosed.Add(natScaleReleaseTimeout)), 0) + natScalePollInterval
	err := wait.PollUntilContextTimeout(ctx, natScalePollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		if ok, err := r.pollPool(ctx); !ok || err != nil {
			return false, err
		}
		switch natScaleReleaseDecision(r.pool, r.preFill.HeldPorts, time.Since(r.lastClosed)) {
		case natScaleReleased:
			return true, nil
		case natScaleReleaseTimedOut:
			return false, errNATScaleRelease
		case natScaleReleaseWait:
		}

		return false, nil
	})
	if err != nil {
		if gwErr := r.checkGateways(ctx); gwErr != nil {
			return gwErr
		}
		if !errors.Is(err, errNATScaleRelease) && !wait.Interrupted(err) {
			return fmt.Errorf("waiting for the release: %w", err)
		}
		connResult := "the connection from a source IP never used before succeeded"
		if connErr := r.tryNeverUsed(ctx); connErr != nil {
			connResult = connErr.Error()
		}

		return fmt.Errorf("%w: %s; %s", errNATScaleRelease, r.releaseProgress(), connResult)
	}
	slog.Info(fmt.Sprintf("Released: %s", r.releaseProgress()))

	return nil
}

// checkAfterRelease checks a connection from the source IP never used, the usual traffic and the gateway pods.
func (r *natScaleRun) checkAfterRelease(ctx context.Context, matrix *ConnectivityMatrix, vpc1, vpc2 *vpcapi.VPC) error {
	if err := r.tryNeverUsed(ctx); err != nil {
		return fmt.Errorf("%w; %s", err, r.releaseProgress())
	}

	tcOpts := r.testCtx.tcOpts
	tcOpts.Sources = natTestProbeServers(matrix, vpc1.Name, vpc2.Name)
	tcOpts.Destinations = tcOpts.Sources
	tcOpts.IPerfsSeconds = min(tcOpts.IPerfsSeconds, gwToggleIPerfSeconds)
	tcOpts.IPerfsMinSpeed = min(tcOpts.IPerfsMinSpeed, gwToggleIPerfMinSpeed)
	if err := r.testCtx.checkGatewayTraffic(ctx, matrix, tcOpts); err != nil {
		return fmt.Errorf("traffic after the pool was released: %w", err)
	}

	return r.checkGateways(ctx)
}

func makeNATScaleSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{Name: "NAT Scale Suite"}
	suite.TestCases = []JUnitTestCase{
		{Name: "Gateway NAT Port Exhaustion", F: gatewayNATPortExhaustionTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// gatewayNATPortExhaustionTest fills the TCP port pool of a masquerade peering on one public IP with short connections
// from one source IP after another, waits for the ports to be released and checks that a source IP never used before
// and the usual traffic get through the gateway again. Only a fill faster than the release fills the pool, which the
// servers of a VLAB don't reach, so it skips on virtual switches.
func gatewayNATPortExhaustionTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.skipFlags.VirtualSwitch {
		return true, nil, fmt.Errorf("%w: measured 45 to 240 connections per second, %d new ports per second needed", errNATScaleVirtualSwitch, natScaleMinPortsPerSec)
	}

	r, err := newNATScaleRun(ctx, testCtx)
	if err != nil {
		return false, nil, err
	}
	if err := r.snapshotGateways(ctx); err != nil {
		return false, nil, err
	}

	vpc1, vpc2, err := firstTwoVPCs(ctx, testCtx.kube)
	if err != nil {
		return errors.Is(err, errNotEnoughVPCs), nil, fmt.Errorf("gateway NAT port exhaustion: %w", err)
	}
	used, err := r.pickServers(matrix, vpc1, vpc2)
	if err != nil {
		return true, nil, err
	}

	skipped, reverts, err := testCtx.runNATTest(ctx, matrix, natScaleSpec())
	if skipped {
		return true, reverts, err
	}
	reverts = append(reverts, r.cleanup(&gwapi.GatewayPeering{Name: fmt.Sprintf("%s--%s", vpc1.Name, vpc2.Name), Namespace: kmetav1.NamespaceDefault}))
	if err != nil {
		return false, reverts, err
	}

	if err := r.readVNIs(ctx, vpc1, vpc2); err != nil {
		return false, reverts, err
	}
	if err := r.connectServers(ctx); err != nil {
		return false, reverts, err
	}
	if err := r.pickSourceIPs(ctx, vpc1, used); err != nil {
		return false, reverts, err
	}
	if err := r.addSourceIPs(ctx); err != nil {
		return false, reverts, err
	}
	if err := r.startListener(ctx); err != nil {
		return false, reverts, err
	}
	if err := r.readPreFill(ctx); err != nil {
		return false, reverts, err
	}
	if err := r.startFill(ctx); err != nil {
		return false, reverts, err
	}
	if err := r.fillPool(ctx); err != nil {
		return false, reverts, err
	}
	if err := r.waitRelease(ctx); err != nil {
		return false, reverts, err
	}

	return false, reverts, r.checkAfterRelease(ctx, matrix, vpc1, vpc2)
}
