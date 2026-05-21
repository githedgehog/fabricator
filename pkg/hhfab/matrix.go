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
	"sync"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/semaphore"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// The connectivity matrix models the expected traffic behavior between every
// pair of test endpoints in a topology. It is populated by generators
// (typically setup-vpcs / setup-peerings) and consumed by a runner that
// exercises each pair.
//
// Design assumptions:
//   - A single matrix represents one steady-state topology. Dynamic changes
//     (overlap-NAT, gateway failover, peering churn) are modeled as a
//     sequence of distinct matrices, not as mutations to one.
//   - Endpoints are canonical: generators allocate one *Endpoint per
//     (server, vpc, subnet) attachment and one per External CRD; the matrix
//     references those pointers from AllEndpoints and as EndpointPair keys.
//   - A server with multiple IPs (attached to several subnets or VPCs) is
//     represented as multiple endpoints, one per (vpc, subnet). The verdict
//     depends on which address is used, so collapsing them is not safe.
//   - Absence of an entry for a pair means default DENY (isolation).
//     Generators may emit explicit Verdict=Deny entries when a Reason aids
//     diagnostics.

// gwNATPortForwardProbeTimeout is the maximum time to wait for the gateway's
// port-forward NAT rule to become active in the dataplane after a peering is
// applied. Unlike fabric route propagation (which waitForNATPoolInLeaves gates
// on), the gateway's DNAT rule programming has its own latency that no
// Kubernetes condition signals.
const gwNATPortForwardProbeTimeout = 2 * time.Minute

// gwNATPortForwardProbeInterval is the polling interval between TCP-reachability probes.
const gwNATPortForwardProbeInterval = 5 * time.Second

// ConnectivityVerdict describes what should happen to traffic on a path.
type ConnectivityVerdict string

const (
	VerdictAllow ConnectivityVerdict = "allow"
	VerdictDeny  ConnectivityVerdict = "deny"
)

// TranslatedAddress describes NAT translation expected on a path. Each field
// is optional; an unset field means "no translation on that axis".
type TranslatedAddress struct {
	// SourcePool: CIDR from which the destination may observe any source IP
	// (masquerade SNAT — the runtime pool selection is not predictable, only
	// the containing range is).
	SourcePool netip.Prefix

	// DestinationIP: IP the source must target to reach the destination
	// (DNAT). Unset means use the destination's real IP.
	DestinationIP netip.Addr

	// DestinationPort: port the destination actually listens on, when
	// different from the source-facing port in
	// ConnectivityExpectation.ProtoPort (port-forward DNAT). Zero means no
	// port translation.
	DestinationPort uint16
}

// ProtoPort is a protocol + port tuple. The zero value (empty protocol,
// port 0) is the sentinel for "applies to the default connectivity check
// the runner performs" (today: ICMP + TCP/any).
type ProtoPort struct {
	Protocol string // "tcp", "udp", "icmp"
	Port     uint16
}

// ConnectivityExpectation describes what should happen on a directional path.
// To express bidirectional behavior, emit two entries with swapped pairs.
type ConnectivityExpectation struct {
	Pair EndpointPair

	// Verdict: should traffic be allowed or denied on this path?
	Verdict ConnectivityVerdict

	// NAT: optional address translation expected on this path.
	NAT *TranslatedAddress

	// Reason: why this expectation exists (diagnostic; the ReachabilityReason
	// enum may be extended with NAT, isolation, and permit-list values as
	// new generators land).
	Reason ReachabilityReason

	// Peering: name of the CRD that produced this expectation (diagnostic).
	Peering string

	// ProtoPort scopes this expectation to a specific protocol/port. The
	// zero value applies to the runner's default check.
	ProtoPort ProtoPort
}

// ServerEndpoint identifies one (server, vpc, subnet) attachment. A server
// attached to multiple subnets is represented by multiple endpoints.
type ServerEndpoint struct {
	Name   string // e.g. "server-1"
	VPC    string // e.g. "vpc-01"
	Subnet string // e.g. "default"

	// HostBGP: if true, this attachment uses BGP to advertise a /32 VIP on
	// the loopback; IP holds that VIP (discovered at runtime). Otherwise IP
	// is the DHCP/static address on the subnet interface.
	HostBGP bool
	IP      netip.Addr
}

// ExternalEndpoint identifies an External CRD.
type ExternalEndpoint struct {
	ExternalName string
	Prefixes     []netip.Prefix

	// SourceIP: optional address used when this external originates traffic.
	// Empty means the external is destination-only in the matrix.
	SourceIP netip.Addr
}

// Endpoint is a tagged union; exactly one of Server, External is non-nil.
type Endpoint struct {
	Server   *ServerEndpoint
	External *ExternalEndpoint
}

// EndpointPair is a directional (source → destination) key. Both fields must
// be non-nil and must point to endpoints listed in the owning matrix's
// AllEndpoints (the matrix uses pointer identity for lookups).
type EndpointPair struct {
	Source      *Endpoint
	Destination *Endpoint
}

// ConnectivityMatrix holds the complete set of expectations for a topology.
type ConnectivityMatrix struct {
	// AllEndpoints: canonical, ordered list of all endpoints in the matrix.
	AllEndpoints []*Endpoint

	// entries[pair][protoPort] = expectation. The zero ProtoPort{} key holds
	// the default-check expectation for the pair.
	entries map[EndpointPair]map[ProtoPort]ConnectivityExpectation
}

// NewConnectivityMatrix returns an empty matrix. AllEndpoints should be set
// by the caller before adding expectations that reference them.
func NewConnectivityMatrix() *ConnectivityMatrix {
	return &ConnectivityMatrix{
		entries: map[EndpointPair]map[ProtoPort]ConnectivityExpectation{},
	}
}

// EndpointPredicate selects endpoints during matrix overlays. Composable
// with the helpers below (ServerInVPC, ExternalNamed).
type EndpointPredicate func(*Endpoint) bool

// ServerInVPC matches server endpoints attached to the given VPC.
func ServerInVPC(vpc string) EndpointPredicate {
	return func(ep *Endpoint) bool {
		return ep.Server != nil && ep.Server.VPC == vpc
	}
}

// ExternalNamed matches external endpoints with the given External CRD name.
func ExternalNamed(name string) EndpointPredicate {
	return func(ep *Endpoint) bool {
		return ep.External != nil && ep.External.ExternalName == name
	}
}

// NATMutator updates a TranslatedAddress for a specific (src, dst) pair.
// The passed-in nat is seeded from any existing entry's NAT (or zero
// value if none) and is the value the helper writes back via matrix.Add.
type NATMutator func(src, dst *Endpoint, nat *TranslatedAddress) error

// OverlayMatrixNAT marks every (src, dst) pair whose endpoints satisfy
// both predicates as Allow, then runs mutator to set NAT info on the
// resulting entry.
//
// Returns an error if no pair matched the predicates, which almost
// always indicates mismatched predicates relative to the matrix's
// AllEndpoints set.
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
				Reason:  ReachabilityReasonGatewayPeering,
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

// BuildConnectivityMatrix assembles a matrix from a pre-discovered set of
// server endpoints (typically returned by SetupVPCs), enumerates external
// endpoints from the live cluster, and populates Allow entries by querying
// IsServerReachable / IsExternalSubnetReachable for every endpoint pair.
// The gatewayEnabled flag is auto-derived from the current Fabricator
// config. NAT translations are not modeled here — callers overlay them on
// the returned matrix before running connectivity tests.
//
// hhfab's CLI / vlabrunner paths don't build a matrix at all; this is
// strictly a test-side opt-in. setupTest calls it once at suite startup;
// matrix-driven tests call Repopulate on the existing matrix after
// DoSetupPeerings to refresh verdicts to the post-peering state.
func BuildConnectivityMatrix(ctx context.Context, kube kclient.Client, serverEndpoints []*Endpoint) (*ConnectivityMatrix, error) {
	matrix := NewConnectivityMatrix()
	matrix.AllEndpoints = append(matrix.AllEndpoints, serverEndpoints...)

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

// Repopulate clears the matrix's expectation entries and refills Allow
// entries by querying the live cluster for reachability between every
// (src, dst) endpoint pair in AllEndpoints. The gatewayEnabled flag is
// derived from the current Fabricator config. NAT translations are
// reset; callers re-apply any overlays after a Repopulate.
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

// Add inserts or replaces the expectation for (Pair, ProtoPort). Generators
// call this to populate the matrix; tests may also call it to override
// individual entries for advanced scenarios.
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

// Lookup returns the expectation for (src, dst, pp). If pp is non-zero and
// no protocol-specific entry exists, falls back to the default ProtoPort{}
// entry. If no entry exists at all, returns a synthetic Verdict=Deny
// expectation (default isolation).
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

// reachabilityFromExpectation projects a matrix expectation onto the
// Reachability struct used by the ping/iperf helpers. The matrix's
// Verdict, Reason, and Peering map directly.
func reachabilityFromExpectation(e ConnectivityExpectation) Reachability {
	return Reachability{
		Reachable: e.Verdict == VerdictAllow,
		Reason:    e.Reason,
		Peering:   e.Peering,
	}
}

// check whether two endpoints belong to the same server / external, regardless
// of the specific IP being tested (in case of multi-homed servers)
func IsSameEndpointNode(a, b *Endpoint) bool {
	if a == nil || b == nil {
		return false
	}

	return (a.External != nil && b.External != nil && a.External.ExternalName == b.External.ExternalName) ||
		(a.Server != nil && b.Server != nil && a.Server.Name == b.Server.Name)
}

// TestConnectivityWithMatrix runs ping/iperf/curl against the topology, using
// the supplied ConnectivityMatrix as the authoritative source for both the
// addresses to target and the expected verdicts. No live reachability
// queries are made; matrix.Lookup is the only oracle.
//
// Server-server pairs run ping always (allow → expect success, deny →
// expect failure) and iperf only when the matrix allows them. Bidirectional
// iperf is detected by looking up the reverse pair in the matrix.
//
// Externals are treated as in the legacy test: one curl per source server
// to a hardcoded environment IP ("1.0.0.1"), with the expectation derived
// from the OR of all (src → *_external) matrix verdicts. External-as-source
// paths are not exercised — the matrix doesn't track them today.
//
// opts.Sources and opts.Destinations filter the matrix iteration by server
// name (matching the legacy TestConnectivity semantics): a non-empty
// Sources restricts the source side, a non-empty Destinations restricts the
// destination side, and bidir only triggers when the reverse pair also
// falls inside the filters.

// matrixTestDeps bundles the shared scaffolding TestConnectivityWithMatrix
// hands to its per-phase helpers: SSH map, concurrency
// semaphores, source/destination filter predicates, the WaitGroup that
// drives goroutine completion, and the error channel that collects probe
// failures.
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

// runMatrixServerServerPhase fans out ping (and iperf3 when allowed)
// goroutines for every (src, dst) server pair the matrix knows about,
// honoring NAT.DestinationIP when present. Pairs that carry a
// DestinationPort are skipped here and instead exercised by
// runMatrixPortForwardPhase, which uses the L4-aware iperf3 helper.
func runMatrixServerServerPhase(ctx context.Context, opts TestConnectivityOpts, matrix *ConnectivityMatrix, deps *matrixTestDeps) error {
	for _, src := range matrix.AllEndpoints {
		if src.Server == nil {
			continue
		}
		if !deps.inSources(src.Server.Name) {
			continue
		}
		for _, dst := range matrix.AllEndpoints {
			if dst.Server == nil || src == dst {
				continue
			}
			// Skip pairs that ultimately point at the same host: same-host
			// traffic short-circuits via lo and does not exercise the fabric.
			if IsSameEndpointNode(src, dst) {
				continue
			}
			if !deps.inDestinations(dst.Server.Name) {
				continue
			}

			entry := matrix.Lookup(src, dst, ProtoPort{})
			// Port-forward destinations (DestinationPort set) are L4-only
			// and handled by runMatrixPortForwardPhase below.
			if entry.NAT != nil && entry.NAT.DestinationPort != 0 {
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

// runMatrixCurlPhase launches one curl per source server in
// deps.inSources to the hardcoded outbound target ("1.0.0.1").
// Each server's expected.Reachable is the OR of all (src → *_external)
// matrix Allow entries that have SNAT info or no NAT at all; DNAT-only
// (port-forward) entries don't generically route outbound and so don't
// raise the expectation.
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
		for _, dst := range matrix.AllEndpoints {
			if dst.External == nil {
				continue
			}
			e := matrix.Lookup(src, dst, ProtoPort{})
			if e.Verdict != VerdictAllow {
				continue
			}
			if e.NAT != nil && !e.NAT.SourcePool.IsValid() {
				continue
			}
			expectedByServer[name] = reachabilityFromExpectation(e)

			break
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

			if ce := checkCurl(ctx, opts, deps.curls, name, ssh, "1.0.0.1", expected.Reachable); ce != nil {
				deps.errChan <- ce
			}
		})
	}
}

// runMatrixPortForwardPhase launches iperf3 against every port-forward
// NAT virtual endpoint encoded in the matrix. External destinations are
// deduped by (srcServer, IP, port) so the single external iperf3 server is hit once;
// server destinations exercise every (src, dst) pair for full cross-product
// coverage.
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
			if src == dst {
				continue
			}
			e := matrix.Lookup(src, dst, ProtoPort{})
			if e.Verdict != VerdictAllow || e.NAT == nil {
				continue
			}
			if !e.NAT.DestinationIP.IsValid() || e.NAT.DestinationPort == 0 {
				continue
			}
			switch {
			case dst.External != nil:
				key := pfTargetKey{from: src.Server.Name, ip: e.NAT.DestinationIP, port: e.NAT.DestinationPort}
				if _, seen := extTargets[key]; seen {
					continue
				}
				extTargets[key] = reachabilityFromExpectation(e)
			case dst.Server != nil:
				if IsSameEndpointNode(src, dst) {
					continue
				}
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

func (c *Config) TestConnectivityWithMatrix(ctx context.Context, vlab *VLAB, opts TestConnectivityOpts, matrix *ConnectivityMatrix) error {
	if matrix == nil {
		return fmt.Errorf("connectivity matrix must be non-nil") //nolint:goerr113
	}
	if opts.PingsCount == 0 && opts.IPerfsSeconds == 0 && opts.CurlsCount == 0 {
		return fmt.Errorf("at least one of pings, iperfs or curls should be enabled") //nolint:goerr113
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

	// Resolve the SSH config and a toolbox mutex for every unique server
	// name referenced by the matrix.
	sshByServer := map[string]*sshutil.Config{}
	toolboxMutexes := map[string]*sync.Mutex{}
	for _, ep := range matrix.AllEndpoints {
		if ep.Server == nil {
			continue
		}
		name := ep.Server.Name
		if _, ok := toolboxMutexes[name]; ok {
			continue
		}
		ssh, ok := sshConfigs[name]
		if !ok {
			return fmt.Errorf("no ssh config for server %q referenced by matrix", name) //nolint:goerr113
		}
		sshByServer[name] = ssh
		toolboxMutexes[name] = &sync.Mutex{}
	}

	n := len(matrix.AllEndpoints)
	errChan := make(chan error, 2*n*n+n)
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

// runMatrixIperfPortForward exercises one port-forward NAT path encoded by
// the matrix: TCP-probe NAT.DestinationIP:Port until the gateway's DNAT
// rule is programmed, then run iperf3 once. Targets may be external NAT
// virtual IPs or other VPCs' NAT pool addresses — the function is agnostic
// to where the (ip, port) lives.
func runMatrixIperfPortForward(ctx context.Context, opts TestConnectivityOpts, iperfs *semaphore.Weighted, from string, ssh *sshutil.Config, toIP netip.Addr, toPort uint16, expected Reachability) *IperfError {
	target := fmt.Sprintf("%s:%d", toIP.String(), toPort)
	logArgs := []any{"from", from, "target", target, "expected", expected.Reachable}
	if expected.Reason != "" {
		logArgs = append(logArgs, "reason", expected.Reason)
	}
	if expected.Peering != "" {
		logArgs = append(logArgs, "peering", expected.Peering)
	}
	slog.Debug("Checking iperf3 through port-forward NAT (matrix)", logArgs...)

	// Gate on TCP reachability: the gateway's port-forward DNAT rule has
	// its own programming lag separate from fabric route propagation, and
	// a successful TCP connect is the precise signal that both halves of
	// the path (fabric route + gateway DNAT) are active. After the probe
	// succeeds, iperf3 runs once and any failure is a real test failure.
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
				ClientMsg:   fmt.Sprintf("port-forward target not reachable after %s: %s", gwNATPortForwardProbeTimeout, lastErr),
			}
		}
		select {
		case <-ctx.Done():
			return &IperfError{Source: from, Destination: target, ClientMsg: ctx.Err().Error()}
		case <-time.After(gwNATPortForwardProbeInterval):
		}
	}

	if err := iperfs.Acquire(ctx, 1); err != nil {
		return &IperfError{Source: from, Destination: target, ClientMsg: fmt.Sprintf("acquiring iperf3 semaphore: %s", err)}
	}
	defer iperfs.Release(1)

	secs := opts.IPerfsSeconds
	cmd := fmt.Sprintf("toolbox -E LD_PRELOAD=/lib/x86_64-linux-gnu/libgcc_s.so.1 -q timeout %d iperf3 -J -c %s -p %d -t %d",
		secs+25, toIP.String(), toPort, secs)
	if _, _, iperfErr := retrySSHCmd(ctx, ssh, cmd, from); iperfErr != nil {
		return &IperfError{Source: from, Destination: target, ClientMsg: iperfErr.Error()}
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
