// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

// Release-time verification of the inbound-ACL feature on external
// attachments, observed end-to-end from the customer side.
//
// Scope:
//
//  1. The hardcoded inbound ACL the agent injects on every non-proxied
//     external attachment, dropping management-plane listener ports
//     (TCP/443, TCP/8080, UDP/67, UDP/161, UDP/4789) destined to the
//     leaf's own IP.
//  2. The user-supplied inbound ACL via ExternalAttachment.Spec.InboundACL
//     (an Action=Discard rule against TCP/2222-myaddr).
//
// Assertion model: probe-only. Each test issues TCP/UDP probes from a
// managed virt-external Flatcar VM into the leaf's external-attachment
// VRF and asserts the customer-visible outcome:
//
//   - TCP: protected ports MUST yield no response (timeout). A RST
//     reply ("Connection refused") means the SYN reached the host
//     stack (broken silicon discard, see PR githedgehog/fabric#1442
//     /#1446 for the hardcoded path; same shape applies to the
//     user-ACL test, which writes Action: ACLActionDiscard against a
//     leaf-myaddr destination and gates on the same silicon behaviour).
//     A completed handshake means the listener was reachable (worst
//     case).
//   - UDP: protected ports MUST yield no listener reply (no UDP
//     datagram returned).
//
// Implementation-tier observations (silicon ACL counters,
// iptables INPUT byte counters, rendered action keyword) are NOT
// asserted in the test: those are silicon-pipeline-dependent and
// vary across Broadcom families (e.g. Tomahawk-class DS5000 traps
// UDP/4789-to-myaddr in its VXLAN parser before IFP, so the ACL
// counter never advances even when the protection is effective).
// All those signals are captured by show-tech on failure
// (sonic-cli show ip access-list, iptables -L INPUT, bcmcmd l3 aacl
// show) for diagnostic purposes.
//
// Skips on virtual switches (VirtualSwitch flag) since the trap-
// precedence behaviour is Broadcom-SAI-specific. Inline-skips when
// no virt-external exists in the vlab inventory (a wiring overlay
// that defines an External + Connection + ExternalAttachment to
// virt-external is required, e.g. lab-ci/envs/.../wiring-port-acl.yaml).

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	probeProtoTCP = "tcp"
	probeProtoUDP = "udp"
)

var errNoInboundACLProbeSource = errors.New("no managed external probe source")

// inboundACLProtectedPort is one entry of the hardcoded deny set.
type inboundACLProtectedPort struct {
	Proto string
	Port  uint16
}

func (p inboundACLProtectedPort) String() string {
	return fmt.Sprintf("%s/%d", p.Proto, p.Port)
}

var inboundACLProtectedPorts = []inboundACLProtectedPort{
	{Proto: probeProtoTCP, Port: 443},
	{Proto: probeProtoTCP, Port: 8080},
	{Proto: probeProtoUDP, Port: 67},
	{Proto: probeProtoUDP, Port: 161},
	{Proto: probeProtoUDP, Port: 4789},
}

// inboundACLUserDenyPort is the leaf-side TCP port the user-ACL test
// blocks. Picked to be outside the hardcoded set above and outside any
// fabric-internal listener (TCP/179 BGP). The leaf has no listener on
// this port, so the kernel TCP stack RSTs a SYN by default; that RST
// is the silicon-vs-host-stack discriminator the test gates on (an
// effective silicon discard suppresses the RST and the probe times
// out).
const inboundACLUserDenyPort uint16 = 2222

// inboundACLUserCatchAllSeq is the explicit permit-any catch-all on
// the user ACL. Mandatory: planHardenedInboundACL drops the implicit
// trailing accept-any once any user statement is present, and without
// the catch-all BGP keepalives over tcp/179 would be denied.
const inboundACLUserCatchAllSeq = 65000

// inboundACLUserDenySeq is the seq number of the user deny rule.
// Must be >= ACLUserMinSeq (10); seq 1-9 are reserved for the
// hardcoded discard set.
const inboundACLUserDenySeq = 10

// inboundACLProbeCount is per-port. Three is enough to disambiguate a
// flaky single result from a stable observation while keeping the test
// light.
const inboundACLProbeCount = 3

// inboundACLProbeTimeout is the per-probe nc / socat timeout in
// seconds. Two seconds is enough to cleanly distinguish "no response"
// from "RST received" on a real network path.
const inboundACLProbeTimeout = 2

// inboundACLAgentSettleTime is how long to wait after a CRD edit for
// the fabricator controller to bump the agent CRD's spec.Generation,
// so a follow-up waitAgentGen actually has something to wait for.
// Pattern lifted from setPortBreakout in rt_utils.go.
const inboundACLAgentSettleTime = 5 * time.Second

// inboundACLTarget binds one external attachment + the leaf and the
// external-side IP that bracket it.
type inboundACLTarget struct {
	ExtName    string // External CRD name = VRF name on virt-external
	AttachName string
	SwitchName string
	LeafIP     string // host part only, no /prefix
	SrcIP      string // external-side IP (Static.RemoteIP or Neighbor.IP)
}

// probeOutcome is the customer-visible result of a single probe burst
// from virt-external to the leaf-side IP. The ordering of values
// reflects severity from the protection-effective baseline:
// Handshake/Reply means the listener was reached, RST means the SYN
// reached the host stack but no listener accepted it (typically a
// host-stack reject or kernel closed-port RST), Timeout/NoReply means
// no response was observed within the probe window.
type probeOutcome int

const (
	probeOutcomeUnknown probeOutcome = iota
	probeOutcomeHandshake
	probeOutcomeRST
	probeOutcomeTimeout
	probeOutcomeReply
	probeOutcomeNoReply
)

func (o probeOutcome) String() string {
	switch o {
	case probeOutcomeHandshake:
		return "handshake"
	case probeOutcomeRST:
		return "rst"
	case probeOutcomeTimeout:
		return "timeout"
	case probeOutcomeReply:
		return "reply"
	case probeOutcomeNoReply:
		return "no-reply"
	case probeOutcomeUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// externalInboundACLTest verifies the hardcoded inbound-ACL discard
// semantics on the chosen non-proxied external attachment by probing
// each protected port from the managed virt-external VM. Customer-
// visible expectation: every TCP probe times out, every UDP probe
// gets no listener reply.
//
// Skips inline if no virt-external is present (no managed probe
// source on this lab). VirtualSwitch skip is set in the suite.
func externalInboundACLTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
	target, err := pickInboundACLTarget(ctx, testCtx.kube, testCtx.vlab)
	if err != nil {
		return errors.Is(err, errNoInboundACLProbeSource), nil, err
	}
	extSSH, err := testCtx.vlabCfg.SSH(ctx, testCtx.vlab, ExternalVMName)
	if err != nil {
		return false, nil, fmt.Errorf("ssh to %s: %w", ExternalVMName, err)
	}

	for _, p := range inboundACLProtectedPorts {
		outcome, diag := probeFromExternal(ctx, extSSH, target, p.Proto, p.Port)
		switch p.Proto {
		case probeProtoTCP:
			if outcome != probeOutcomeTimeout {
				return false, nil, fmt.Errorf("%s probe to %s on %s: expected timeout, got %s%s", //nolint:goerr113
					p, target.LeafIP, target.SwitchName, outcome, diagSuffix(diag))
			}
		case probeProtoUDP:
			if outcome != probeOutcomeNoReply {
				return false, nil, fmt.Errorf("%s probe to %s on %s: expected no-reply, got %s%s", //nolint:goerr113
					p, target.LeafIP, target.SwitchName, outcome, diagSuffix(diag))
			}
		}
	}

	return false, nil, nil
}

// externalInboundUserACLTest verifies that a user-supplied
// Spec.InboundACL Action=Discard rule on the external attachment
// renders an effective silicon discard for matching traffic, observed
// end-to-end via probe behaviour.
//
// Steps: apply seq 10 Action=Discard tcp/2222 + seq 65000 Action=Permit
// any, wait for the agent, probe TCP/2222 from virt-external, assert
// probe outcome is timeout (no response). A RST outcome means the SYN
// reached the host stack: silicon Discard did not cancel the IP2ME
// copy, the host either RST'd the closed port or iptables REJECTed it;
// both are failures of the silicon-level protection. A completed
// handshake would mean a listener accepted the connection, also a
// failure.
func externalInboundUserACLTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
	target, err := pickInboundACLTarget(ctx, testCtx.kube, testCtx.vlab)
	if err != nil {
		return errors.Is(err, errNoInboundACLProbeSource), nil, err
	}
	extSSH, err := testCtx.vlabCfg.SSH(ctx, testCtx.vlab, ExternalVMName)
	if err != nil {
		return false, nil, fmt.Errorf("ssh to %s: %w", ExternalVMName, err)
	}
	key := kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: target.AttachName}

	att := &vpcapi.ExternalAttachment{}
	if err := testCtx.kube.Get(ctx, key, att); err != nil {
		return false, nil, fmt.Errorf("getting attachment %s: %w", target.AttachName, err)
	}
	originalACL := att.Spec.InboundACL.DeepCopy()

	revert := func(ctx context.Context) error {
		fresh := &vpcapi.ExternalAttachment{}
		if err := kclient.IgnoreNotFound(testCtx.kube.Get(ctx, key, fresh)); err != nil {
			return fmt.Errorf("getting attachment %s for revert: %w", target.AttachName, err)
		}
		if fresh.Name == "" {
			return nil
		}
		fresh.Spec.InboundACL = originalACL
		if err := updateAndWaitAgent(ctx, testCtx, target.SwitchName, fresh); err != nil {
			return fmt.Errorf("restoring InboundACL on %s: %w", target.AttachName, err)
		}

		return nil
	}
	reverts := []RevertFunc{revert}

	att.Spec.InboundACL = userInboundACLSpec(target.LeafIP)
	if err := updateAndWaitAgent(ctx, testCtx, target.SwitchName, att); err != nil {
		return false, reverts, fmt.Errorf("applying user InboundACL on %s: %w", target.AttachName, err)
	}

	outcome, diag := probeFromExternal(ctx, extSSH, target, probeProtoTCP, inboundACLUserDenyPort)
	if outcome != probeOutcomeTimeout {
		return false, reverts, fmt.Errorf("tcp/%d probe to %s on %s: expected timeout, got %s%s", //nolint:goerr113
			inboundACLUserDenyPort, target.LeafIP, target.SwitchName, outcome, diagSuffix(diag))
	}

	return false, reverts, nil
}

// pickInboundACLTarget picks the non-proxied external attachment whose
// peer is a virtual External in the wiring (i.e. its switch port is
// cabled to virt-external). On a HLAB env that has not loaded the
// wiring-port-acl overlay this returns an error and the caller must
// inline-skip the test.
func pickInboundACLTarget(ctx context.Context, kube kclient.Client, vlab *VLAB) (inboundACLTarget, error) {
	if vlab == nil {
		return inboundACLTarget{}, fmt.Errorf("%w: vlab inventory is nil", errNoInboundACLProbeSource)
	}
	hasVirtExternal := false
	for _, vm := range vlab.VMs {
		if vm.Name == ExternalVMName {
			hasVirtExternal = true

			break
		}
	}
	if !hasVirtExternal {
		return inboundACLTarget{}, fmt.Errorf("%w: %s VM not in inventory", errNoInboundACLProbeSource, ExternalVMName)
	}

	exts := &vpcapi.ExternalList{}
	if err := kube.List(ctx, exts); err != nil {
		return inboundACLTarget{}, fmt.Errorf("listing externals: %w", err)
	}
	attaches := &vpcapi.ExternalAttachmentList{}
	if err := kube.List(ctx, attaches); err != nil {
		return inboundACLTarget{}, fmt.Errorf("listing external attachments: %w", err)
	}
	virtExts := map[string]bool{}
	for _, ext := range exts.Items {
		if !isHardware(&ext) {
			virtExts[ext.Name] = true
		}
	}
	if len(virtExts) == 0 {
		return inboundACLTarget{}, fmt.Errorf("%w: no virtual External in wiring", errNoInboundACLProbeSource)
	}

	for _, att := range attaches.Items {
		if !virtExts[att.Spec.External] {
			continue
		}
		if att.Spec.Static != nil && att.Spec.Static.Proxy {
			continue
		}

		conn := &wiringapi.Connection{}
		if err := kube.Get(ctx, kclient.ObjectKey{
			Namespace: kmetav1.NamespaceDefault, Name: att.Spec.Connection,
		}, conn); err != nil {
			return inboundACLTarget{}, fmt.Errorf("getting connection %s: %w", att.Spec.Connection, err)
		}
		if conn.Spec.External == nil {
			continue
		}

		var leafCIDR, srcIP string
		if att.Spec.Static != nil {
			leafCIDR = att.Spec.Static.IP
			srcIP = att.Spec.Static.RemoteIP
		} else {
			leafCIDR = att.Spec.Switch.IP
			srcIP = att.Spec.Neighbor.IP
		}
		leafPrefix, err := netip.ParsePrefix(leafCIDR)
		if err != nil {
			return inboundACLTarget{}, fmt.Errorf("parsing leaf IP %q on attachment %s: %w", leafCIDR, att.Name, err)
		}

		return inboundACLTarget{
			ExtName:    att.Spec.External,
			AttachName: att.Name,
			SwitchName: conn.Spec.External.Link.Switch.DeviceName(),
			LeafIP:     leafPrefix.Addr().String(),
			SrcIP:      srcIP,
		}, nil
	}

	return inboundACLTarget{}, fmt.Errorf("%w: no non-proxied external attachment with a virtual External peer", errNoInboundACLProbeSource)
}

// userInboundACLSpec returns the user ACL applied by the user-ACL test:
// discard TCP/<UserDenyPort> to the leaf-myaddr at seq 10, plus the
// mandatory permit-any catch-all at seq 65000. Discard (not Deny) is
// the silicon-drop action; Deny would render as host-stack Drop and
// the kernel would still RST the SYN, defeating the test. Without the
// catch-all, once any user statement is present the planner removes
// the implicit accept-any and BGP keepalives over tcp/179 (and any
// other non-matching traffic) get denied by the gNMI default deny.
func userInboundACLSpec(leafIP string) *vpcapi.ACLSpec {
	return &vpcapi.ACLSpec{
		Statements: []vpcapi.ACLStatement{
			{
				Seq:            inboundACLUserDenySeq,
				Action:         vpcapi.ACLActionDiscard,
				Protocol:       vpcapi.ACLProtocolTCP,
				SrcPrefix:      vpcapi.ACLAny,
				DstPrefix:      leafIP + "/32",
				PortRangeBegin: inboundACLUserDenyPort,
				PortRangeEnd:   inboundACLUserDenyPort,
			},
			{
				Seq:       inboundACLUserCatchAllSeq,
				Action:    vpcapi.ACLActionPermit,
				Protocol:  vpcapi.ACLProtocolIP,
				SrcPrefix: vpcapi.ACLAny,
				DstPrefix: vpcapi.ACLAny,
			},
		},
	}
}

// updateAndWaitAgent updates a CRD and then waits for the affected
// switch's agent to actually apply the change. The pattern (capture
// generation, kube.Update, brief sleep so the controller bumps
// agent.Generation, waitAgentGen, WaitReady for stability) is the
// same one used by setPortBreakout: needed because WaitReady alone
// returns immediately if the controller has not yet bumped the agent
// CRD's Generation when the wait starts.
func updateAndWaitAgent(ctx context.Context, testCtx *VPCPeeringTestCtx, switchName string, obj kclient.Object) error {
	currGen, err := getAgentGen(ctx, testCtx.kube, switchName)
	if err != nil {
		return fmt.Errorf("getting agent generation for %s: %w", switchName, err)
	}
	if err := testCtx.kube.Update(ctx, obj); err != nil {
		return fmt.Errorf("updating %s: %w", obj.GetName(), err)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting for controller to bump agent generation: %w", ctx.Err())
	case <-time.After(inboundACLAgentSettleTime):
	}
	if err := waitAgentGen(ctx, testCtx.kube, switchName, currGen); err != nil {
		return fmt.Errorf("waiting for agent on %s to apply past gen %d: %w", switchName, currGen, err)
	}
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return fmt.Errorf("waiting for switches ready: %w", err)
	}

	return nil
}

// probeFromExternal sends inboundACLProbeCount probes from
// virt-external to the leaf-side IP using nc (TCP) or socat (UDP)
// under `ip vrf exec`, returning the strongest customer-visible
// outcome observed across the burst.
//
// For TCP, severity ordering is: Handshake (listener reached) > RST
// (host stack reachable, no listener accepted) > Timeout (no
// response). For UDP it's: Reply (listener replied) > NoReply (no
// datagram returned). SSH transport errors on individual probes are
// tolerated and skip that iteration; if every probe errors the
// outcome is Unknown and the assertion fails.
//
// The second return is a diagnostic snippet (last ssh error or last
// raw probe output that did not parse), populated only when the
// outcome is Unknown so callers can surface why no positive signal
// came back.
func probeFromExternal(ctx context.Context, ssh *sshutil.Config, t inboundACLTarget, proto string, port uint16) (probeOutcome, string) {
	sawHandshake, sawRST, sawTimeout := false, false, false
	sawReply, sawNoReply := false, false
	var lastDiag string
	for range inboundACLProbeCount {
		out, raw, err := runProbeOnce(ctx, ssh, t, proto, port)
		if err != nil {
			lastDiag = fmt.Sprintf("ssh err: %v; raw: %q", err, raw)

			continue
		}
		switch out {
		case probeOutcomeHandshake:
			sawHandshake = true
		case probeOutcomeRST:
			sawRST = true
		case probeOutcomeTimeout:
			sawTimeout = true
		case probeOutcomeReply:
			sawReply = true
		case probeOutcomeNoReply:
			sawNoReply = true
		case probeOutcomeUnknown:
			lastDiag = fmt.Sprintf("unparsed probe output: %q", raw)
		}
	}
	switch proto {
	case probeProtoTCP:
		switch {
		case sawHandshake:
			return probeOutcomeHandshake, ""
		case sawRST:
			return probeOutcomeRST, ""
		case sawTimeout:
			return probeOutcomeTimeout, ""
		}
	case probeProtoUDP:
		switch {
		case sawReply:
			return probeOutcomeReply, ""
		case sawNoReply:
			return probeOutcomeNoReply, ""
		}
	}

	return probeOutcomeUnknown, lastDiag
}

// runProbeOnce runs a single TCP or UDP probe from virt-external,
// sourced from the right VRF, against the leaf-side IP, and returns
// the parsed customer-visible outcome.
//
// Bash on Flatcar is built without --enable-net-redirections, so the
// `/dev/tcp` and `/dev/udp` builtins are unavailable. We use nc -zvw
// for TCP (the verbose stderr distinguishes "succeeded" from
// "Connection refused" from "timed out") and socat for UDP (write a
// payload, read with a timeout; empty stdout = no listener
// response, non-empty = listener replied).
//
// Why socat for UDP and not nc: OpenBSD nc reports "succeeded" for any
// UDP target that does not return ICMP-port-unreachable, so
// `nc -uzvw` cannot distinguish "listener ignored / drop" from
// "listener replied". socat's read-after-send loop gives the positive
// evidence the test needs.
//
// The whole script runs as a single program inside `ip vrf exec`.
// Wrapping in `bash -c` ensures every syscall in the probe inherits
// the VRF binding; otherwise the destination route lookup happens in
// the default routing table.
func runProbeOnce(ctx context.Context, ssh *sshutil.Config, t inboundACLTarget, proto string, port uint16) (probeOutcome, string, error) {
	var script string
	switch proto {
	case probeProtoTCP:
		script = fmt.Sprintf(
			`set +e; nc -zvw%d %s %d 2>&1; echo EXIT=$?`,
			inboundACLProbeTimeout, t.LeafIP, port,
		)
	case probeProtoUDP:
		// printf 'x' (one byte) is required: empty stdin to socat means
		// no datagram is ever sent. socat exit 127 (or stderr matching
		// "command not found" / "no such file" / "permission denied")
		// is treated as a probe-error, not as NO_REPLY, so a missing or
		// unrunnable socat cannot false-pass the assertion.
		script = fmt.Sprintf(
			`set +e; ef=$(mktemp); out=$(printf x | socat -t %d - UDP:%s:%d 2>"$ef"); rc=$?; err=$(cat "$ef"); rm -f "$ef"; if [ -n "$out" ]; then echo GOT_REPLY; elif [ "$rc" = 0 ]; then echo NO_REPLY; else echo "PROBE_ERROR rc=$rc err=$err"; fi`,
			inboundACLProbeTimeout, t.LeafIP, port,
		)
	default:
		return probeOutcomeUnknown, "", fmt.Errorf("unknown protocol %q", proto) //nolint:goerr113
	}
	cmd := fmt.Sprintf("sudo ip vrf exec %s bash -c %s", shellSingleQuote(t.ExtName), shellSingleQuote(script))
	stdout, stderr, err := ssh.Run(ctx, cmd)
	out := strings.TrimSpace(stdout + "\n" + stderr)

	switch proto {
	case probeProtoTCP:
		switch {
		case strings.Contains(out, "EXIT=0"), strings.Contains(out, "succeeded"):
			return probeOutcomeHandshake, out, nil
		case strings.Contains(out, "Connection refused"):
			return probeOutcomeRST, out, nil
		case strings.Contains(out, "timed out"), strings.Contains(out, "Operation now in progress"):
			return probeOutcomeTimeout, out, nil
		}
	case probeProtoUDP:
		switch {
		case strings.Contains(out, "GOT_REPLY"):
			return probeOutcomeReply, out, nil
		case strings.Contains(out, "NO_REPLY"):
			return probeOutcomeNoReply, out, nil
		case strings.Contains(out, "PROBE_ERROR"):
			return probeOutcomeUnknown, out, nil
		}
	}
	if err != nil {
		return probeOutcomeUnknown, out, fmt.Errorf("probe ssh: %w", err)
	}

	return probeOutcomeUnknown, out, nil
}

// shellSingleQuote single-quotes a string for safe inclusion in a
// remote shell command.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// diagSuffix returns " (<diag>)" if diag is non-empty, otherwise "".
// Used to attach the last probe-error / unparsed output to the
// outcome-mismatch error message when probeFromExternal could not
// classify any probe in the burst.
func diagSuffix(diag string) string {
	if diag == "" {
		return ""
	}

	return " (" + diag + ")"
}
