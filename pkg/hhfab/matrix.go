// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/semaphore"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// The connectivity matrix models the expected traffic behavior between every
// pair of test endpoints in a topology.
// Design assumptions:
//   - A server with multiple IPs (attached to several subnets or VPCs) is
//     represented as multiple endpoints, one per (vpc, subnet). The verdict
//     depends on which address is used.
//   - Absence of an entry for a pair means default DENY (isolation).

// gwNATPortForwardProbeTimeout is the maximum time to wait for the gateway's
// port-forward NAT rule to become active in the dataplane after a peering is
// applied.
const gwNATPortForwardProbeTimeout = 2 * time.Minute

// gwNATPortForwardProbeInterval is the polling interval between TCP-reachability probes.
const gwNATPortForwardProbeInterval = 5 * time.Second

// Bound the wait for an on-demand iperf3 listener to bind its port (~5s total).
// The interval is a remote shell sleep argument, in seconds.
const (
	protoPortListenerBindAttempts = 20
	protoPortListenerBindSleepArg = "0.25"
)

type ConnectivityVerdict string

const (
	VerdictAllow   ConnectivityVerdict = "allow"
	VerdictDeny    ConnectivityVerdict = "deny"
	VerdictUnknown ConnectivityVerdict = "unknown"
)

// TranslatedAddress describes NAT translation expected on a path. Each field
// is optional; an unset field means "no translation on that axis".
type TranslatedAddress struct {
	// SourcePool: CIDR from which the destination may observe any source IP
	SourcePool netip.Prefix

	// DestinationIP: IP the source must target to reach the destination
	DestinationIP netip.Addr

	// DestinationPort: port the destination actually listens on, when
	// different from the source-facing port. Zero means no port translation.
	DestinationPort uint16
}

type ProtoPort struct {
	Protocol string // "tcp", "udp", "icmp"
	Port     uint16
}

// ConnectivityExpectation describes what should happen on a directional path.
type ConnectivityExpectation struct {
	Pair EndpointPair

	Verdict ConnectivityVerdict

	NAT *TranslatedAddress

	Reason ReachabilityReason

	Peering string

	// free-form context for Reason, currently only set on VerdictUnknown entries
	Detail string

	ProtoPort ProtoPort
}

type ServerEndpoint struct {
	Name    string // e.g. "server-1"
	VPC     string // e.g. "vpc-01"
	Subnet  string // e.g. "default"
	HostBGP bool
	IP      netip.Addr
}

type ExternalEndpoint struct {
	ExternalName string
	Prefixes     []netip.Prefix
	SourceIP     netip.Addr
}

// Endpoint is a tagged union; exactly one of Server, External is non-nil.
type Endpoint struct {
	Server   *ServerEndpoint
	External *ExternalEndpoint
}

type EndpointPair struct {
	Source      *Endpoint
	Destination *Endpoint
}

type ConnectivityMatrix struct {
	// Canonical, ordered list of all endpoints in the matrix.
	AllEndpoints []*Endpoint

	// The zero ProtoPort{} key holds the default-check expectation for the pair.
	entries map[EndpointPair]map[ProtoPort]ConnectivityExpectation

	// Attachments/addresses endpoint discovery could not turn into endpoints.
	// Kept here so Validate can refuse to test a topology it only partially sees.
	dropped []DroppedEndpoint
}

func NewConnectivityMatrix() *ConnectivityMatrix {
	return &ConnectivityMatrix{
		entries: map[EndpointPair]map[ProtoPort]ConnectivityExpectation{},
	}
}

type EndpointPredicate func(*Endpoint) bool

func ServerInVPC(vpc string) EndpointPredicate {
	return func(ep *Endpoint) bool {
		return ep.Server != nil && ep.Server.VPC == vpc
	}
}

func ExternalNamed(name string) EndpointPredicate {
	return func(ep *Endpoint) bool {
		return ep.External != nil && ep.External.ExternalName == name
	}
}

// overlayReason keeps the reason populate already established for a pair
// when an overlay rewrites its verdict. Only pairs populate could not see
// (it has no entry, or could not evaluate one) fall back to gateway
// peering — which is what every overlay models today: NAT is a gateway
// feature, and a pair the switch fabric already allows is not overlaid.
func overlayReason(existing ReachabilityReason) ReachabilityReason {
	if existing != "" {
		return existing
	}

	return ReachabilityReasonGatewayPeering
}

type NATMutator func(src, dst *Endpoint, nat *TranslatedAddress) error

func OverlayMatrixNAT(
	matrix *ConnectivityMatrix,
	srcPred, dstPred EndpointPredicate,
	mutator NATMutator,
) error {
	var touched int
	for _, src := range matrix.AllEndpoints {
		if !srcPred(src) {
			continue
		}
		for _, dst := range matrix.AllEndpoints {
			if !dstPred(dst) {
				continue
			}
			existing := matrix.Lookup(src, dst, ProtoPort{})
			nat := TranslatedAddress{}
			if existing.NAT != nil {
				nat = *existing.NAT
			}
			if err := mutator(src, dst, &nat); err != nil {
				return err
			}
			matrix.Add(ConnectivityExpectation{
				Pair:    EndpointPair{Source: src, Destination: dst},
				Verdict: VerdictAllow,
				Reason:  overlayReason(existing.Reason),
				Peering: existing.Peering,
				NAT:     &nat,
			})
			touched++
		}
	}
	if touched == 0 {
		return fmt.Errorf("matrix overlay applied to no entries (check predicates)") //nolint:goerr113
	}

	return nil
}

func BuildConnectivityMatrix(ctx context.Context, kube kclient.Client, serverEndpoints []*Endpoint, dropped []DroppedEndpoint) (*ConnectivityMatrix, error) {
	matrix := NewConnectivityMatrix()
	matrix.AllEndpoints = append(matrix.AllEndpoints, serverEndpoints...)
	matrix.dropped = append(matrix.dropped, dropped...)

	externalList := vpcapi.ExternalList{}
	if err := kube.List(ctx, &externalList); err != nil {
		return nil, fmt.Errorf("listing externals for connectivity matrix: %w", err)
	}
	matrix.AllEndpoints = append(matrix.AllEndpoints, buildExternalEndpoints(externalList.Items)...)

	if err := matrix.Repopulate(ctx, kube); err != nil {
		return nil, err
	}

	return matrix, nil
}

func BuildConnectivityMatrixFromCluster(ctx context.Context, kube kclient.Client, ssh SSHResolver) (*ConnectivityMatrix, error) {
	endpoints, dropped, err := CollectServerEndpoints(ctx, kube, ssh, nil)
	if err != nil {
		return nil, fmt.Errorf("collecting server endpoints for matrix: %w", err)
	}

	return BuildConnectivityMatrix(ctx, kube, endpoints, dropped)
}

// Repopulate clears the matrix's expectation entries and refills Allow
// entries by querying the live cluster for reachability between every
// (src, dst) endpoint pair in AllEndpoints.
func (m *ConnectivityMatrix) Repopulate(ctx context.Context, kube kclient.Client) error {
	f, _, _, err := fab.GetFabAndNodes(ctx, kube, fab.GetFabAndNodesOpts{AllowNotHydrated: true})
	if err != nil {
		return fmt.Errorf("getting fab for matrix repopulate: %w", err)
	}
	if err := populateConnectivityMatrix(ctx, kube, m, f.Spec.Config.Gateway.Enable); err != nil {
		return fmt.Errorf("populating connectivity matrix: %w", err)
	}

	return nil
}

// Add inserts or replaces the expectation for (Pair, ProtoPort).
func (m *ConnectivityMatrix) Add(e ConnectivityExpectation) {
	if m.entries == nil {
		m.entries = map[EndpointPair]map[ProtoPort]ConnectivityExpectation{}
	}
	byPP, ok := m.entries[e.Pair]
	if !ok {
		byPP = map[ProtoPort]ConnectivityExpectation{}
		m.entries[e.Pair] = byPP
	}
	byPP[e.ProtoPort] = e
}

func (m *ConnectivityMatrix) Lookup(src, dst *Endpoint, pp ProtoPort) ConnectivityExpectation {
	pair := EndpointPair{Source: src, Destination: dst}
	if byPP, ok := m.entries[pair]; ok {
		if e, ok := byPP[pp]; ok {
			return e
		}
		if pp != (ProtoPort{}) {
			if e, ok := byPP[ProtoPort{}]; ok {
				return e
			}
		}
	}

	return ConnectivityExpectation{
		Pair:      pair,
		Verdict:   VerdictDeny,
		ProtoPort: pp,
	}
}

func (m *ConnectivityMatrix) Validate() error {
	if m == nil {
		return fmt.Errorf("connectivity matrix is nil") //nolint:goerr113
	}

	var errs []error

	if len(m.AllEndpoints) == 0 {
		errs = append(errs, fmt.Errorf("matrix has no endpoints")) //nolint:goerr113
	}

	if len(m.dropped) > 0 {
		reasons := make([]string, 0, len(m.dropped))
		for _, d := range m.dropped {
			reasons = append(reasons, d.String())
		}
		slices.Sort(reasons)
		errs = append(errs, fmt.Errorf("endpoint discovery dropped %d attachment(s)/address(es), topology only partially visible: %s", //nolint:goerr113
			len(m.dropped), strings.Join(reasons, "; ")))
	}

	unknowns := []string{}
	unprobed := []string{}
	for _, byPP := range m.entries {
		for _, e := range byPP {
			owner := m.entryOwner(e)
			if owner == matrixPhaseUnprobed {
				unprobed = append(unprobed, describeMatrixEntry(e))

				continue
			}
			if owner != matrixPhaseSkipped && e.Verdict == VerdictUnknown {
				unknowns = append(unknowns, describeMatrixEntry(e))
			}
		}
	}
	if len(unknowns) > 0 {
		slices.Sort(unknowns)
		errs = append(errs, fmt.Errorf("%d matrix entries could not be evaluated and were not overlaid by the test: %s", //nolint:goerr113
			len(unknowns), strings.Join(unknowns, "; ")))
	}
	if len(unprobed) > 0 {
		slices.Sort(unprobed)
		errs = append(errs, fmt.Errorf("%d matrix entries no probe phase can read, so they would pass unasserted: %s", //nolint:goerr113
			len(unprobed), strings.Join(unprobed, "; ")))
	}

	return errors.Join(errs...)
}

// matrixPhase is the probe phase that owns an entry. Single source of truth for
// the phases' skip gates and for Validate. Ownership is not the same as being
// probed: the --source/--destination filters gate pairs on top of it, and
// Validate does not model them, so an owned entry whose pair those filters
// exclude is never probed.
type matrixPhase int

const (
	// nothing reads the entry, and nothing makes that intentional
	matrixPhaseUnprobed matrixPhase = iota
	// nothing reads the entry by design: self-pair, or default entry superseded
	// by the pair's proto-port entries
	matrixPhaseSkipped
	matrixPhaseServerServer
	matrixPhasePortForward
	matrixPhaseProtoPort
	matrixPhaseCurl
)

// entryOwner assumes Validate has already rejected VerdictUnknown: the phases
// carry no verdict guard of their own, and reachabilityFromExpectation maps
// Unknown to Reachable:false, so a phase reached without Validate would assert
// deny on an entry explicitly marked unevaluable.
func (m *ConnectivityMatrix) entryOwner(e ConnectivityExpectation) matrixPhase {
	src, dst := e.Pair.Source, e.Pair.Destination
	if src == nil || dst == nil || src.Server == nil {
		return matrixPhaseUnprobed
	}
	if src == dst || IsSameEndpointNode(src, dst) {
		return matrixPhaseSkipped
	}
	// a translated port makes the path L4-only
	portForward := e.NAT != nil && e.NAT.DestinationPort != 0
	if portForward && !e.NAT.DestinationIP.IsValid() {
		return matrixPhaseUnprobed
	}
	scoped := e.ProtoPort != (ProtoPort{})
	// the proto-port probes dial ProtoPort.Port, so they would aim past the
	// translation, and the port-forward probe carries no protocol dimension
	if portForward && scoped {
		return matrixPhaseUnprobed
	}

	switch {
	case dst.Server != nil:
		switch {
		case scoped:
			return matrixPhaseProtoPort
		case portForward:
			return matrixPhasePortForward
		case m.HasProtoPortEntries(src, dst):
			return matrixPhaseSkipped
		default:
			return matrixPhaseServerServer
		}
	case dst.External != nil:
		switch {
		case scoped:
			// the curl probe carries no protocol/port dimension and
			// runMatrixProtoPortPhase only probes server destinations
			return matrixPhaseUnprobed
		case portForward:
			return matrixPhasePortForward
		case e.Verdict == VerdictAllow && e.NAT != nil && !e.NAT.SourcePool.IsValid():
			// the curl oracle discards a NAT with no source pool, so it would
			// assert the opposite of what this entry claims
			return matrixPhaseUnprobed
		case e.Verdict == VerdictUnknown && m.hasExternalCurlAllow(src):
			// one untargeted curl per source, so a sibling external's Allow
			// already pins its outcome
			return matrixPhaseSkipped
		default:
			return matrixPhaseCurl
		}
	default:
		return matrixPhaseUnprobed
	}
}

// checkProbesEnabled rejects a run whose enabled probes cannot assert every
// entry a phase will read: checkPing/checkIPerf/checkCurl no-op when their count
// is off, so those entries would pass unasserted.
//
// It walks endpoint pairs rather than m.entries because that is what the phases
// do, and populate only stores reachable or unevaluable pairs — so a topology
// whose pairs are all denies has no stored entries at all. The source and
// destination gates mirror deps.inSources/inDestinations, which the phases apply
// on top of ownership.
func (m *ConnectivityMatrix) checkProbesEnabled(opts TestConnectivityOpts) error {
	var serverServer, external, icmp int
	for _, src := range m.AllEndpoints {
		if src.Server == nil || (len(opts.Sources) > 0 && !slices.Contains(opts.Sources, src.Server.Name)) {
			continue
		}
		for _, dst := range m.AllEndpoints {
			// an external destination has no server name to filter on
			if dst.Server != nil && len(opts.Destinations) > 0 && !slices.Contains(opts.Destinations, dst.Server.Name) {
				continue
			}
			entries := m.ProtoPortEntries(src, dst)
			// plus the pair's default entry, which Lookup synthesizes as a Deny
			// when populate stored nothing for it
			entries = append(entries, m.Lookup(src, dst, ProtoPort{}))
			for _, e := range entries {
				owner := m.entryOwner(e)
				if owner == matrixPhaseServerServer {
					serverServer++
				}
				if owner == matrixPhaseCurl {
					external++
				}
				if owner == matrixPhaseProtoPort && e.ProtoPort.Protocol == "icmp" {
					icmp++
				}
			}
		}
	}

	if opts.PingsCount <= 0 && opts.IPerfsSeconds <= 0 && serverServer > 0 {
		return fmt.Errorf("matrix has %d server-to-server entries but both pings and iperfs are disabled", serverServer) //nolint:goerr113
	}
	if opts.CurlsCount <= 0 && external > 0 {
		return fmt.Errorf("matrix has %d external entries but curls are disabled", external) //nolint:goerr113
	}
	if opts.PingsCount <= 0 && icmp > 0 {
		return fmt.Errorf("matrix has %d icmp proto-port entries but pings are disabled", icmp) //nolint:goerr113
	}

	return nil
}

func (m *ConnectivityMatrix) hasExternalCurlAllow(src *Endpoint) bool {
	_, ok := m.externalCurlAllowed(src)

	return ok
}

func (m *ConnectivityMatrix) externalCurlAllowed(src *Endpoint) (ConnectivityExpectation, bool) {
	if src == nil || src.Server == nil {
		return ConnectivityExpectation{}, false
	}
	for _, dst := range m.AllEndpoints {
		if dst.External == nil {
			continue
		}
		e := m.Lookup(src, dst, ProtoPort{})
		if e.Verdict != VerdictAllow {
			continue
		}
		if e.NAT != nil && !e.NAT.SourcePool.IsValid() {
			continue
		}

		return e, true
	}

	return ConnectivityExpectation{}, false
}

func describeMatrixEntry(e ConnectivityExpectation) string {
	out := fmt.Sprintf("%s → %s", endpointLabel(e.Pair.Source), endpointLabel(e.Pair.Destination))
	if e.ProtoPort != (ProtoPort{}) {
		out += fmt.Sprintf(" [%s/%d]", e.ProtoPort.Protocol, e.ProtoPort.Port)
	}
	if e.Detail != "" {
		out += ": " + e.Detail
	} else if e.Reason != "" {
		out += ": " + string(e.Reason)
	}

	return out
}

func (m *ConnectivityMatrix) ProtoPortEntries(src, dst *Endpoint) []ConnectivityExpectation {
	byPP, ok := m.entries[EndpointPair{Source: src, Destination: dst}]
	if !ok {
		return nil
	}
	out := make([]ConnectivityExpectation, 0, len(byPP))
	for pp, e := range byPP {
		if pp == (ProtoPort{}) {
			continue
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b ConnectivityExpectation) int {
		if a.ProtoPort.Protocol != b.ProtoPort.Protocol {
			return strings.Compare(a.ProtoPort.Protocol, b.ProtoPort.Protocol)
		}

		return int(a.ProtoPort.Port) - int(b.ProtoPort.Port)
	})

	return out
}

func endpointLabel(ep *Endpoint) string {
	switch {
	case ep == nil:
		return "<nil>"
	case ep.Server != nil:
		return fmt.Sprintf("%s(%s/%s)", ep.Server.Name, ep.Server.VPC, ep.Server.Subnet)
	case ep.External != nil:
		return "external:" + ep.External.ExternalName
	default:
		return "<empty>"
	}
}

func (m *ConnectivityMatrix) HasProtoPortEntries(src, dst *Endpoint) bool {
	byPP, ok := m.entries[EndpointPair{Source: src, Destination: dst}]
	if !ok {
		return false
	}
	for pp := range byPP {
		if pp != (ProtoPort{}) {
			return true
		}
	}

	return false
}

func reachabilityFromExpectation(e ConnectivityExpectation) Reachability {
	return Reachability{
		Reachable: e.Verdict == VerdictAllow,
		Reason:    e.Reason,
		Peering:   e.Peering,
	}
}

func IsSameEndpointNode(a, b *Endpoint) bool {
	if a == nil || b == nil {
		return false
	}

	return (a.External != nil && b.External != nil && a.External.ExternalName == b.External.ExternalName) ||
		(a.Server != nil && b.Server != nil && a.Server.Name == b.Server.Name)
}

type matrixTestDeps struct {
	sshByServer    map[string]*sshutil.Config
	pings          *semaphore.Weighted
	iperfs         *semaphore.Weighted
	curls          *semaphore.Weighted
	inSources      func(string) bool
	inDestinations func(string) bool
	wg             *sync.WaitGroup
	errChan        chan<- error
}

// probesServerPair reports whether the server-to-server phases will probe this
// pair, so listener setup covers exactly what gets probed.
func (d *matrixTestDeps) probesServerPair(src, dst *Endpoint) bool {
	return src.Server != nil && dst.Server != nil &&
		d.inSources(src.Server.Name) && d.inDestinations(dst.Server.Name)
}

func runMatrixServerServerPhase(ctx context.Context, opts TestConnectivityOpts, matrix *ConnectivityMatrix, deps *matrixTestDeps) error {
	for _, src := range matrix.AllEndpoints {
		if src.Server == nil {
			continue
		}
		if !deps.inSources(src.Server.Name) {
			continue
		}
		for _, dst := range matrix.AllEndpoints {
			if dst.Server == nil {
				continue
			}
			if !deps.inDestinations(dst.Server.Name) {
				continue
			}

			entry := matrix.Lookup(src, dst, ProtoPort{})
			if matrix.entryOwner(entry) != matrixPhaseServerServer {
				continue
			}

			// Resolve the target IP: a static DNAT entry replaces the
			// destination's real IP with the NAT pool address the source
			// is expected to target.
			toIP := dst.Server.IP
			if entry.NAT != nil && entry.NAT.DestinationIP.IsValid() {
				toIP = entry.NAT.DestinationIP
			}
			if !toIP.IsValid() {
				return fmt.Errorf("matrix entry %s→%s (vpc %s/%s) has no valid target IP", src.Server.Name, dst.Server.Name, dst.Server.VPC, dst.Server.Subnet) //nolint:goerr113
			}

			expected := reachabilityFromExpectation(entry)
			bidir := false
			if opts.IPerfsSeconds > 0 && expected.Reachable && deps.inSources(dst.Server.Name) && deps.inDestinations(src.Server.Name) {
				reverse := matrix.Lookup(dst, src, ProtoPort{})
				if reverse.Verdict == VerdictAllow {
					// bidir iperf3 uses one TCP session; both halves share
					// a target IP. Any DNAT on either side breaks that
					// symmetry, so fall back to two separate sessions.
					forwardDNAT := entry.NAT != nil && entry.NAT.DestinationIP.IsValid()
					reverseDNAT := reverse.NAT != nil && reverse.NAT.DestinationIP.IsValid()
					if !forwardDNAT && !reverseDNAT {
						bidir = true
					}
				}
			}

			args := pingIperfPairArgs{
				From:     src.Server.Name,
				To:       dst.Server.Name,
				FromSSH:  deps.sshByServer[src.Server.Name],
				ToIP:     toIP,
				Expected: expected,
				Bidir:    bidir,
				Pings:    deps.pings,
				Iperfs:   deps.iperfs,
			}
			deps.wg.Go(func() {
				for _, e := range runPingIperfPair(ctx, opts, args) {
					deps.errChan <- e
				}
			})
		}
	}

	return nil
}

func runMatrixCurlPhase(ctx context.Context, opts TestConnectivityOpts, matrix *ConnectivityMatrix, deps *matrixTestDeps) {
	expectedByServer := map[string]Reachability{}
	for _, src := range matrix.AllEndpoints {
		if src.Server == nil {
			continue
		}
		name := src.Server.Name
		if !deps.inSources(name) {
			continue
		}
		if _, seen := expectedByServer[name]; !seen {
			expectedByServer[name] = Reachability{}
		}
		if expectedByServer[name].Reachable {
			continue
		}
		if e, ok := matrix.externalCurlAllowed(src); ok {
			expectedByServer[name] = reachabilityFromExpectation(e)
		}
	}

	for name, ssh := range deps.sshByServer {
		if !deps.inSources(name) {
			continue
		}
		expected := expectedByServer[name]
		deps.wg.Go(func() {
			logArgs := []any{"from", name, "expected", expected.Reachable}
			if expected.Reachable {
				logArgs = append(logArgs, "reason", expected.Reason)
				if expected.Peering != "" {
					logArgs = append(logArgs, "peering", expected.Peering)
				}
			}
			slog.Debug("Checking external connectivity", logArgs...)

			if ce := checkCurl(ctx, opts, deps.curls, name, ssh, "1.0.0.1", expected); ce != nil {
				deps.errChan <- ce
			}
		})
	}
}

func runMatrixPortForwardPhase(ctx context.Context, opts TestConnectivityOpts, matrix *ConnectivityMatrix, deps *matrixTestDeps) {
	type pfTargetKey struct {
		from string
		ip   netip.Addr
		port uint16
	}
	extTargets := map[pfTargetKey]Reachability{}
	for _, src := range matrix.AllEndpoints {
		if src.Server == nil {
			continue
		}
		if !deps.inSources(src.Server.Name) {
			continue
		}
		for _, dst := range matrix.AllEndpoints {
			e := matrix.Lookup(src, dst, ProtoPort{})
			if matrix.entryOwner(e) != matrixPhasePortForward {
				continue
			}
			switch {
			case dst.External != nil:
				key := pfTargetKey{from: src.Server.Name, ip: e.NAT.DestinationIP, port: e.NAT.DestinationPort}
				// Several externals can share one port-forward virtual
				// (IP, port); an Allow anywhere in that set wins over a
				// Deny, since a single probe can't distinguish them.
				if prev, seen := extTargets[key]; seen && (prev.Reachable || e.Verdict != VerdictAllow) {
					continue
				}
				extTargets[key] = reachabilityFromExpectation(e)
			case dst.Server != nil:
				if !deps.inDestinations(dst.Server.Name) {
					continue
				}
				expected := reachabilityFromExpectation(e)
				target := e.NAT.DestinationIP
				port := e.NAT.DestinationPort
				fromName := src.Server.Name
				deps.wg.Go(func() {
					if ie := runMatrixIperfPortForward(ctx, opts, deps.iperfs, fromName, deps.sshByServer[fromName], target, port, expected); ie != nil {
						deps.errChan <- ie
					}
				})
			}
		}
	}
	for key, val := range extTargets {
		deps.wg.Go(func() {
			if ie := runMatrixIperfPortForward(ctx, opts, deps.iperfs, key.from, deps.sshByServer[key.from], key.ip, key.port, val); ie != nil {
				deps.errChan <- ie
			}
		})
	}
}

// persistentIperf3Port is the port the always-on iperf3 -s daemon serves (TCP+UDP).
const persistentIperf3Port = 5201

func startMatrixProtoPortListeners(ctx context.Context, matrix *ConnectivityMatrix, deps *matrixTestDeps) (func(), error) {
	type hostPort struct {
		host string
		port uint16
	}
	wanted := map[hostPort]struct{}{}
	for _, src := range matrix.AllEndpoints {
		for _, dst := range matrix.AllEndpoints {
			if !deps.probesServerPair(src, dst) {
				continue
			}
			for _, e := range matrix.ProtoPortEntries(src, dst) {
				if matrix.entryOwner(e) != matrixPhaseProtoPort {
					continue
				}
				pp := e.ProtoPort
				if pp.Protocol != "tcp" && pp.Protocol != "udp" {
					continue
				}
				if pp.Port == 0 || pp.Port == persistentIperf3Port {
					continue
				}
				wanted[hostPort{host: dst.Server.Name, port: pp.Port}] = struct{}{}
			}
		}
	}

	started := make([]hostPort, 0, len(wanted))
	teardown := func() {
		// Teardown must run even when the caller's ctx has been canceled
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, hp := range started {
			ssh := deps.sshByServer[hp.host]
			if ssh == nil {
				continue
			}
			// anchored so port 5301 does not match a listener on 53010
			cmd := fmt.Sprintf("sudo docker exec iperf3 pkill -f 'iperf3 -s -p %d$'", hp.port)
			if _, stderr, err := retrySSHCmd(tctx, ssh, cmd, hp.host); err != nil {
				slog.Warn("Failed to stop proto-port iperf3 listener", "host", hp.host, "port", hp.port, "err", err, "stderr", stderr)
			}
		}
	}

	for hp := range wanted {
		ssh := deps.sshByServer[hp.host]
		if ssh == nil {
			teardown()

			return nil, fmt.Errorf("no ssh config for server %q needed as proto-port listener", hp.host) //nolint:goerr113
		}
		// docker exec -d returns as soon as the process is spawned, so wait for
		// the listener to actually bind.
		cmd := fmt.Sprintf("sudo docker exec -d iperf3 iperf3 -s -p %d && for i in $(seq 1 %d); do nc -z 127.0.0.1 %d && exit 0; sleep %s; done; exit 1",
			hp.port, protoPortListenerBindAttempts, hp.port, protoPortListenerBindSleepArg)
		started = append(started, hp)
		if _, stderr, err := retrySSHCmd(ctx, ssh, cmd, hp.host); err != nil {
			teardown()

			return nil, fmt.Errorf("starting proto-port iperf3 listener on %s:%d: %w: %s", hp.host, hp.port, err, stderr)
		}
		slog.Debug("Started proto-port iperf3 listener", "host", hp.host, "port", hp.port)
	}

	return teardown, nil
}

// runMatrixProtoPortPhase exercises every non-zero ProtoPort matrix entry with a
// protocol-specific probe: "icmp" → ping, "tcp" → nc connect, "udp" → iperf3 -u
// loss check.
func runMatrixProtoPortPhase(ctx context.Context, opts TestConnectivityOpts, matrix *ConnectivityMatrix, deps *matrixTestDeps) {
	for _, src := range matrix.AllEndpoints {
		for _, dst := range matrix.AllEndpoints {
			if !deps.probesServerPair(src, dst) {
				continue
			}
			for _, entry := range matrix.ProtoPortEntries(src, dst) {
				if matrix.entryOwner(entry) != matrixPhaseProtoPort {
					continue
				}
				expected := reachabilityFromExpectation(entry)
				pp := entry.ProtoPort
				fromName := src.Server.Name
				toName := dst.Server.Name
				fromSSH := deps.sshByServer[fromName]
				toIP := dst.Server.IP
				if entry.NAT != nil && entry.NAT.DestinationIP.IsValid() {
					toIP = entry.NAT.DestinationIP
				}
				if !toIP.IsValid() {
					deps.errChan <- fmt.Errorf("matrix proto entry %s→%s (%s/%d) has no valid target IP", fromName, toName, pp.Protocol, pp.Port) //nolint:goerr113

					continue
				}

				switch pp.Protocol {
				case "icmp":
					deps.wg.Go(func() {
						if pe := checkPing(ctx, opts.PingsCount, deps.pings, fromName, toName, fromSSH, toIP, nil, expected); pe != nil {
							deps.errChan <- pe
						}
					})
				case "tcp":
					port := pp.Port
					deps.wg.Go(func() {
						if ie := checkTCPPort(ctx, deps.iperfs, fromName, fromSSH, toIP, port, expected); ie != nil {
							deps.errChan <- ie
						}
					})
				case "udp":
					port := pp.Port
					deps.wg.Go(func() {
						if ie := checkUDPPort(ctx, opts, deps.iperfs, fromName, fromSSH, toIP, port, expected); ie != nil {
							deps.errChan <- ie
						}
					})
				default:
					deps.errChan <- fmt.Errorf("matrix proto entry %s→%s has unsupported protocol %q", fromName, toName, pp.Protocol) //nolint:goerr113
				}
			}
		}
	}
}

func (c *Config) TestConnectivityWithMatrix(ctx context.Context, vlab *VLAB, opts TestConnectivityOpts, matrix *ConnectivityMatrix) error {
	if matrix == nil {
		return fmt.Errorf("connectivity matrix must be non-nil") //nolint:goerr113
	}
	if opts.PingsCount == 0 && opts.IPerfsSeconds == 0 && opts.CurlsCount == 0 {
		return fmt.Errorf("at least one of pings, iperfs or curls should be enabled") //nolint:goerr113
	}
	if err := matrix.Validate(); err != nil {
		return fmt.Errorf("connectivity matrix is not a sound oracle: %w", err)
	}
	if err := matrix.checkProbesEnabled(opts); err != nil {
		return err
	}
	start := time.Now()

	if opts.PingsParallel <= 0 {
		opts.PingsParallel = 50
	}
	if opts.IPerfsParallel <= 0 {
		opts.IPerfsParallel = 1
	}
	if opts.CurlsParallel <= 0 {
		opts.CurlsParallel = 50
	}

	slog.Info("Testing connectivity from matrix", "endpoints", len(matrix.AllEndpoints))

	sshConfigs, _, cacheCancel, err := c.prepareConnectivityTest(ctx, vlab, &opts)
	if err != nil {
		return err
	}
	defer cacheCancel()

	sshByServer := map[string]*sshutil.Config{}
	for _, ep := range matrix.AllEndpoints {
		if ep.Server == nil {
			continue
		}
		name := ep.Server.Name
		if _, ok := sshByServer[name]; ok {
			continue
		}
		ssh, ok := sshConfigs[name]
		if !ok {
			return fmt.Errorf("no ssh config for server %q referenced by matrix", name) //nolint:goerr113
		}
		sshByServer[name] = ssh
	}

	n := len(matrix.AllEndpoints)
	protoEntries := 0
	for _, src := range matrix.AllEndpoints {
		for _, dst := range matrix.AllEndpoints {
			if src == dst {
				continue
			}
			protoEntries += len(matrix.ProtoPortEntries(src, dst))
		}
	}
	errChan := make(chan error, 2*n*n+n+protoEntries)
	deps := &matrixTestDeps{
		sshByServer: sshByServer,
		pings:       semaphore.NewWeighted(opts.PingsParallel),
		iperfs:      semaphore.NewWeighted(opts.IPerfsParallel),
		curls:       semaphore.NewWeighted(opts.CurlsParallel),
		inSources: func(name string) bool {
			return len(opts.Sources) == 0 || slices.Contains(opts.Sources, name)
		},
		inDestinations: func(name string) bool {
			return len(opts.Destinations) == 0 || slices.Contains(opts.Destinations, name)
		},
		wg:      &sync.WaitGroup{},
		errChan: errChan,
	}

	teardownListeners := func() {}
	if opts.PingsCount > 0 || opts.IPerfsSeconds > 0 {
		td, err := startMatrixProtoPortListeners(ctx, matrix, deps)
		if err != nil {
			return err
		}
		teardownListeners = td
	}
	defer teardownListeners()

	if opts.PingsCount > 0 || opts.IPerfsSeconds > 0 {
		if err := runMatrixServerServerPhase(ctx, opts, matrix, deps); err != nil {
			return err
		}
	}
	if opts.CurlsCount > 0 {
		runMatrixCurlPhase(ctx, opts, matrix, deps)
	}
	if opts.IPerfsSeconds > 0 {
		runMatrixPortForwardPhase(ctx, opts, matrix, deps)
	}
	if opts.PingsCount > 0 || opts.IPerfsSeconds > 0 {
		runMatrixProtoPortPhase(ctx, opts, matrix, deps)
	}

	deps.wg.Wait()
	close(errChan)

	var joined error
	var numPingErrs, numIperfErrs, numCurlErrs int
	for e := range errChan {
		var (
			pingErr  *PingError
			iperfErr *IperfError
			curlErr  *CurlError
		)
		switch {
		case errors.As(e, &pingErr):
			numPingErrs++
		case errors.As(e, &iperfErr):
			numIperfErrs++
		case errors.As(e, &curlErr):
			numCurlErrs++
		}
		joined = errors.Join(joined, e)
	}

	if joined != nil {
		slog.Error("Test connectivity (matrix) failed", "ping", numPingErrs, "iperf", numIperfErrs, "curl", numCurlErrs, "took", time.Since(start), "errors", joined)
	} else {
		slog.Info("Test connectivity (matrix) passed", "took", time.Since(start))
	}

	return joined
}

func runMatrixIperfPortForward(ctx context.Context, opts TestConnectivityOpts, iperfs *semaphore.Weighted, from string, ssh *sshutil.Config, toIP netip.Addr, toPort uint16, expected Reachability) *IperfError {
	target := fmt.Sprintf("%s:%d", toIP.String(), toPort)
	why := expectationWhy(expected)
	logArgs := []any{"from", from, "target", target, "expected", expected.Reachable}
	if expected.Reason != "" {
		logArgs = append(logArgs, "reason", expected.Reason)
	}
	if expected.Peering != "" {
		logArgs = append(logArgs, "peering", expected.Peering)
	}
	slog.Debug("Checking iperf3 through port-forward NAT (matrix)", logArgs...)

	if !expected.Reachable {
		return checkTCPPort(ctx, nil, from, ssh, toIP, toPort, expected)
	}

	// Gate on TCP reachability: a successful TCP connect is the precise signal
	// that both halves of the path (fabric route + gateway DNAT) are active.
	probe := fmt.Sprintf("nc -zw2 %s %d", toIP.String(), toPort)
	deadline := time.Now().Add(gwNATPortForwardProbeTimeout)
	var lastErr error
	for {
		if _, _, err := retrySSHCmd(ctx, ssh, probe, from); err == nil {
			break
		} else { //nolint:revive
			lastErr = err
		}
		if time.Now().After(deadline) {
			return &IperfError{
				Source:      from,
				Destination: target,
				Why:         why,
				ClientMsg:   fmt.Sprintf("port-forward target not reachable after %s: %s", gwNATPortForwardProbeTimeout, lastErr),
			}
		}
		select {
		case <-ctx.Done():
			return &IperfError{Source: from, Destination: target, Why: why, ClientMsg: ctx.Err().Error()}
		case <-time.After(gwNATPortForwardProbeInterval):
		}
	}

	if err := iperfs.Acquire(ctx, 1); err != nil {
		return &IperfError{Source: from, Destination: target, Why: why, ClientMsg: fmt.Sprintf("acquiring iperf3 semaphore: %s", err)}
	}
	defer iperfs.Release(1)

	secs := opts.IPerfsSeconds
	cmd := fmt.Sprintf("toolbox -E LD_PRELOAD=/lib/x86_64-linux-gnu/libgcc_s.so.1 -q timeout %d iperf3 -J -c %s -p %d -t %d",
		secs+25, toIP.String(), toPort, secs)
	if _, _, iperfErr := retrySSHCmd(ctx, ssh, cmd, from); iperfErr != nil {
		return &IperfError{Source: from, Destination: target, Why: why, ClientMsg: iperfErr.Error()}
	}

	return nil
}

func DoVLABTestConnectivityWithMatrix(ctx context.Context, workDir, cacheDir string, opts TestConnectivityOpts, matrix *ConnectivityMatrix) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.TestConnectivityWithMatrix(ctx, vlab, opts, matrix)
}
