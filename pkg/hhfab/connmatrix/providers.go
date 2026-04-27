// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package connmatrix

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// CalculateStaticNATIP computes a server's static-NAT pool address using the
// dataplane algorithm: nat_ip = nat_pool_start + (source_ip - source_subnet_start).
// Callers supply the source IP (the server's real IP on its VPC), the source
// subnet's network address, and the NAT pool's network address.
func CalculateStaticNATIP(sourceIP, sourceSubnet, natPoolStart netip.Addr) (netip.Addr, error) {
	if !sourceIP.Is4() || !sourceSubnet.Is4() || !natPoolStart.Is4() {
		return netip.Addr{}, fmt.Errorf("only IPv4 NAT is currently supported") //nolint:err113
	}

	sourceBytes := sourceIP.As4()
	subnetBytes := sourceSubnet.As4()

	sourceInt := binary.BigEndian.Uint32(sourceBytes[:])
	subnetInt := binary.BigEndian.Uint32(subnetBytes[:])

	if sourceInt < subnetInt {
		return netip.Addr{}, fmt.Errorf("source IP %s is before subnet start %s", sourceIP, sourceSubnet) //nolint:err113
	}

	offset := sourceInt - subnetInt

	natPoolBytes := natPoolStart.As4()
	natPoolInt := binary.BigEndian.Uint32(natPoolBytes[:])
	natIPInt := natPoolInt + offset

	var natIPBytes [4]byte
	binary.BigEndian.PutUint32(natIPBytes[:], natIPInt)

	return netip.AddrFrom4(natIPBytes), nil
}

// ConnectivityProvider produces expectations from API objects. Providers are
// composed by the MatrixBuilder and run in order; each receives the matrix
// built by earlier providers so later providers can refine or override.
type ConnectivityProvider interface {
	Name() string
	BuildExpectations(ctx context.Context, kube kclient.Reader, endpoints []Endpoint, current *ConnectivityMatrix) ([]ConnectivityExpectation, error)
}

// IntraVPCProvider emits expectations for endpoint pairs on the same VPC based
// on subnet isolation/restriction and the VPC's Permit list. It mirrors the
// semantics of apiutil.IsSubnetReachableWithinVPCObj.
type IntraVPCProvider struct{}

func (p *IntraVPCProvider) Name() string { return "intra-vpc" }

func (p *IntraVPCProvider) BuildExpectations(ctx context.Context, kube kclient.Reader, endpoints []Endpoint, _ *ConnectivityMatrix) ([]ConnectivityExpectation, error) {
	vpcs, err := listVPCs(ctx, kube)
	if err != nil {
		return nil, err
	}

	out := []ConnectivityExpectation{}
	for i := range endpoints {
		for j := range endpoints {
			if i == j {
				continue
			}
			src, dst := endpoints[i], endpoints[j]
			if src.External || dst.External {
				continue
			}
			srcVPC, srcSubnet, ok := strings.Cut(src.Subnet, "/")
			if !ok {
				continue
			}
			dstVPC, dstSubnet, ok := strings.Cut(dst.Subnet, "/")
			if !ok {
				continue
			}
			if srcVPC != dstVPC {
				continue
			}
			vpc, ok := vpcs[srcVPC]
			if !ok {
				return nil, fmt.Errorf("VPC %q not found", srcVPC) //nolint:err113
			}
			reachable, err := apiutil.IsSubnetReachableWithinVPCObj(vpc, srcSubnet, dstSubnet)
			if err != nil {
				return nil, fmt.Errorf("intra-VPC reachability %s→%s: %w", src.Subnet, dst.Subnet, err)
			}
			if !reachable {
				continue
			}
			out = append(out, ConnectivityExpectation{
				Source:      src.Key(),
				Destination: dst.Key(),
				Verdict:     VerdictAllow,
				Direction:   DirectionBidirectional,
				Reason:      ReachabilityReasonIntraVPC,
			})
		}
	}

	return out, nil
}

// SwitchPeeringProvider emits expectations for endpoint pairs on different VPCs
// that are reachable via a VPCPeering. Mirrors apiutil.IsSubnetReachableBetweenVPCs
// semantics but iterates per-endpoint so multi-VPC trunking works correctly.
type SwitchPeeringProvider struct{}

func (p *SwitchPeeringProvider) Name() string { return "switch-peering" }

func (p *SwitchPeeringProvider) BuildExpectations(ctx context.Context, kube kclient.Reader, endpoints []Endpoint, _ *ConnectivityMatrix) ([]ConnectivityExpectation, error) {
	vpcs, err := listVPCs(ctx, kube)
	if err != nil {
		return nil, err
	}
	peerings := &vpcapi.VPCPeeringList{}
	if err := kube.List(ctx, peerings, kclient.InNamespace(kmetav1.NamespaceDefault)); err != nil {
		return nil, fmt.Errorf("listing VPC peerings: %w", err)
	}

	remoteNonEmpty := map[string]bool{}
	for i := range peerings.Items {
		vp := &peerings.Items[i]
		if vp.Spec.Remote == "" {
			continue
		}
		ne, err := apiutil.IsVPCPeeringRemoteNotEmpty(ctx, kube, vp)
		if err != nil {
			return nil, fmt.Errorf("checking remote for VPCPeering %s: %w", vp.Name, err)
		}
		remoteNonEmpty[vp.Name] = ne
	}

	out := []ConnectivityExpectation{}
	for i := range endpoints {
		for j := range endpoints {
			if i == j {
				continue
			}
			src, dst := endpoints[i], endpoints[j]
			if src.External || dst.External {
				continue
			}
			srcVPC, srcSubnet, ok := strings.Cut(src.Subnet, "/")
			if !ok {
				continue
			}
			dstVPC, dstSubnet, ok := strings.Cut(dst.Subnet, "/")
			if !ok {
				continue
			}
			if srcVPC == dstVPC {
				continue
			}
			// Existence check keeps behaviour identical to apiutil: any missing
			// VPC/subnet aborts with an error rather than silently returning
			// DENY, and the VPC lookup guards against nil-deref on endpoints
			// whose Subnet references a VPC the API list didn't return.
			srcVPCObj, ok := vpcs[srcVPC]
			if !ok {
				return nil, fmt.Errorf("VPC %q not found", srcVPC) //nolint:err113
			}
			dstVPCObj, ok := vpcs[dstVPC]
			if !ok {
				return nil, fmt.Errorf("VPC %q not found", dstVPC) //nolint:err113
			}
			if _, ok := srcVPCObj.Spec.Subnets[srcSubnet]; !ok {
				return nil, fmt.Errorf("subnet %s not found in VPC %s", srcSubnet, srcVPC) //nolint:err113
			}
			if _, ok := dstVPCObj.Spec.Subnets[dstSubnet]; !ok {
				return nil, fmt.Errorf("subnet %s not found in VPC %s", dstSubnet, dstVPC) //nolint:err113
			}

			peeringName := ""
			for _, vp := range peerings.Items {
				if vp.Spec.Remote != "" && !remoteNonEmpty[vp.Name] {
					continue
				}
				if !peeringCoversVPCs(&vp, srcVPC, dstVPC) {
					continue
				}
				for _, permit := range vp.Spec.Permit {
					p1, ok := permit[srcVPC]
					if !ok {
						continue
					}
					p2, ok := permit[dstVPC]
					if !ok {
						continue
					}
					if (len(p1.Subnets) != 0 && !slices.Contains(p1.Subnets, srcSubnet)) ||
						(len(p2.Subnets) != 0 && !slices.Contains(p2.Subnets, dstSubnet)) {
						continue
					}
					peeringName = vp.Name

					break
				}
				if peeringName != "" {
					break
				}
			}
			if peeringName == "" {
				continue
			}
			out = append(out, ConnectivityExpectation{
				Source:      src.Key(),
				Destination: dst.Key(),
				Verdict:     VerdictAllow,
				Direction:   DirectionBidirectional,
				Reason:      ReachabilityReasonSwitchPeering,
				Peering:     peeringName,
			})
		}
	}

	return out, nil
}

func peeringCoversVPCs(vp *vpcapi.VPCPeering, vpc1, vpc2 string) bool {
	for _, permit := range vp.Spec.Permit {
		_, a := permit[vpc1]
		_, b := permit[vpc2]
		if a && b {
			return true
		}
	}

	return false
}

// GatewayPeeringProvider emits expectations for endpoint pairs reachable via
// a GatewayPeering. This replaces the "temporary" IsSubnetReachableWithGatewayPeering
// helper and adds NAT-aware translation plus direction tracking.
type GatewayPeeringProvider struct{}

func (p *GatewayPeeringProvider) Name() string { return "gateway-peering" }

func (p *GatewayPeeringProvider) BuildExpectations(ctx context.Context, kube kclient.Reader, endpoints []Endpoint, current *ConnectivityMatrix) ([]ConnectivityExpectation, error) {
	vpcInfos := &gwapi.VPCInfoList{}
	if err := kube.List(ctx, vpcInfos, kclient.InNamespace(kmetav1.NamespaceDefault)); err != nil {
		return nil, fmt.Errorf("listing VPCInfos: %w", err)
	}
	infoByName := map[string]*gwapi.VPCInfo{}
	for i := range vpcInfos.Items {
		info := &vpcInfos.Items[i]
		infoByName[info.Name] = info
	}

	peerings := &gwapi.GatewayPeeringList{}
	if err := kube.List(ctx, peerings, kclient.InNamespace(kmetav1.NamespaceDefault)); err != nil {
		return nil, fmt.Errorf("listing GatewayPeerings: %w", err)
	}

	endpointsByVPC := map[string][]Endpoint{}
	for _, ep := range endpoints {
		if ep.External {
			continue
		}
		vpcName, _, ok := strings.Cut(ep.Subnet, "/")
		if !ok {
			continue
		}
		endpointsByVPC[vpcName] = append(endpointsByVPC[vpcName], ep)
	}

	out := []ConnectivityExpectation{}
	for i := range peerings.Items {
		gp := &peerings.Items[i]
		vpcs := make([]string, 0, len(gp.Spec.Peering))
		for name := range gp.Spec.Peering {
			vpcs = append(vpcs, name)
		}
		if len(vpcs) != 2 {
			continue
		}
		// Skip external peerings in Phase 1 — curl path still handles externals.
		if strings.HasPrefix(vpcs[0], gwapi.VPCInfoExtPrefix) || strings.HasPrefix(vpcs[1], gwapi.VPCInfoExtPrefix) {
			continue
		}
		exps, err := buildGatewayPairExpectations(gp, vpcs[0], vpcs[1], endpointsByVPC, infoByName)
		if err != nil {
			return nil, fmt.Errorf("gateway peering %s: %w", gp.Name, err)
		}
		out = append(out, exps...)
	}

	return out, nil
}

// buildGatewayPairExpectations produces expectations for both directions of one
// peering between vpcA and vpcB. For each (srcServer, dstServer) pair where the
// source is exposed in its own PeeringEntry and the destination is exposed in
// its own, emit one expectation per direction, with the source's NAT applied
// to the return path and the destination's NAT applied to the forward path.
func buildGatewayPairExpectations(
	gp *gwapi.GatewayPeering,
	vpcA, vpcB string,
	endpointsByVPC map[string][]Endpoint,
	infoByName map[string]*gwapi.VPCInfo,
) ([]ConnectivityExpectation, error) {
	entryA := gp.Spec.Peering[vpcA]
	entryB := gp.Spec.Peering[vpcB]
	infoA, okA := infoByName[vpcA]
	infoB, okB := infoByName[vpcB]
	if !okA || !okB {
		return nil, fmt.Errorf("missing VPCInfo for %s or %s", vpcA, vpcB) //nolint:err113
	}

	out := []ConnectivityExpectation{}
	// A → B uses B's expose (DNAT targets, destination side) and A's expose (SNAT, source side).
	forward, err := buildGatewayDirection(gp.Name, vpcA, vpcB, entryA, entryB, infoA, infoB, endpointsByVPC)
	if err != nil {
		return nil, err
	}
	out = append(out, forward...)
	// B → A uses A's expose on destination and B's expose on source.
	reverse, err := buildGatewayDirection(gp.Name, vpcB, vpcA, entryB, entryA, infoB, infoA, endpointsByVPC)
	if err != nil {
		return nil, err
	}
	out = append(out, reverse...)

	return out, nil
}

// buildGatewayDirection emits src→dst expectations for one direction of a
// peering. It applies the NAT direction rules:
//
//   - srcEntry.NAT.PortForward means src is a target, not an initiator, so
//     this direction (src→dst) is NOT allowed via this peering.
//   - dstEntry.NAT.Masquerade means dst is the initiator; src cannot initiate
//     toward dst, so this direction is NOT allowed.
//   - All other combinations are allowed. Static NAT on either side adds DNAT
//     at the destination; masquerade on src adds SNAT. Port-forward on dst
//     adds DNAT with per-port mappings.
func buildGatewayDirection(
	peeringName, srcVPC, dstVPC string,
	srcEntry, dstEntry *gwapi.PeeringEntry,
	srcInfo, dstInfo *gwapi.VPCInfo,
	endpointsByVPC map[string][]Endpoint,
) ([]ConnectivityExpectation, error) {
	if srcEntry == nil || dstEntry == nil {
		return nil, nil
	}
	srcEndpoints := endpointsByVPC[srcVPC]
	dstEndpoints := endpointsByVPC[dstVPC]
	if len(srcEndpoints) == 0 || len(dstEndpoints) == 0 {
		return nil, nil
	}

	out := []ConnectivityExpectation{}
	for _, src := range srcEndpoints {
		srcExpose, srcSNAT, err := matchServerExpose(srcEntry, srcInfo, src)
		if err != nil {
			return nil, err
		}
		if srcExpose == nil {
			continue
		}
		// src is a NAT target (port-forward destination) — it cannot initiate
		// across this peering, so emit nothing for this direction.
		if srcExpose.NAT != nil && srcExpose.NAT.PortForward != nil {
			continue
		}
		for _, dst := range dstEndpoints {
			dstExpose, dstDNAT, err := matchServerExpose(dstEntry, dstInfo, dst)
			if err != nil {
				return nil, err
			}
			if dstExpose == nil {
				continue
			}
			// dst uses masquerade — dst is the initiator, src cannot reach it.
			if dstExpose.NAT != nil && dstExpose.NAT.Masquerade != nil {
				continue
			}

			out = append(out, ConnectivityExpectation{
				Source:      src.Key(),
				Destination: dst.Key(),
				Verdict:     VerdictAllow,
				Direction:   DirectionForward,
				NAT:         mergeNAT(srcSNAT, dstDNAT),
				Reason:      ReachabilityReasonGatewayPeering,
				Peering:     peeringName,
			})
		}
	}

	return out, nil
}

// mergeNAT combines SNAT info from the source side with DNAT info from the
// destination side. Either may be nil. Returns nil when both are nil.
func mergeNAT(srcSNAT, dstDNAT *TranslatedAddress) *TranslatedAddress {
	if srcSNAT == nil && dstDNAT == nil {
		return nil
	}
	out := &TranslatedAddress{}
	if srcSNAT != nil {
		out.SourceIP = srcSNAT.SourceIP
		out.SourcePool = srcSNAT.SourcePool
	}
	if dstDNAT != nil {
		out.DestinationIP = dstDNAT.DestinationIP
		out.PortForwards = dstDNAT.PortForwards
	}

	return out
}

// matchServerExpose finds the first expose entry in `entry` that includes the
// server's subnet, and computes the NAT translation that applies to that
// server. Returns nil, nil, nil when the server is not exposed by this entry.
//
// The returned TranslatedAddress is the per-server projection of the expose's
// NAT mode. For a source-side call it describes SNAT (SourceIP/SourcePool); for
// a destination-side call it describes DNAT (DestinationIP/PortForwards). The
// caller decides how to interpret the result.
func matchServerExpose(entry *gwapi.PeeringEntry, info *gwapi.VPCInfo, ep Endpoint) (*gwapi.PeeringEntryExpose, *TranslatedAddress, error) {
	_, subName, ok := strings.Cut(ep.Subnet, "/")
	if !ok {
		return nil, nil, fmt.Errorf("bad endpoint subnet %q", ep.Subnet) //nolint:err113
	}
	subInfo, ok := info.Spec.Subnets[subName]
	if !ok {
		return nil, nil, nil
	}
	subnetCIDR, err := netip.ParsePrefix(subInfo.CIDR)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing subnet CIDR %q: %w", subInfo.CIDR, err)
	}

	for i := range entry.Expose {
		expose := &entry.Expose[i]
		if expose.DefaultDestination {
			// "default" is a catch-all sink; no per-server NAT.
			return expose, nil, nil
		}
		if !exposeIncludesSubnet(expose, info, subName) {
			continue
		}
		translated, err := resolveNATTranslation(expose, ep.IP, subnetCIDR)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving NAT for %s: %w", ep.Key(), err)
		}

		return expose, translated, nil
	}

	return nil, nil, nil
}

// exposeIncludesSubnet reports whether the expose entry's IPs list includes
// the given subnet (by VPCSubnet name or by CIDR match).
func exposeIncludesSubnet(expose *gwapi.PeeringEntryExpose, info *gwapi.VPCInfo, subName string) bool {
	subInfo, ok := info.Spec.Subnets[subName]
	if !ok {
		return false
	}
	for _, ip := range expose.IPs {
		switch {
		case ip.VPCSubnet != "":
			if ip.VPCSubnet == subName {
				return true
			}
		case ip.CIDR != "":
			if ip.CIDR == subInfo.CIDR {
				return true
			}
		}
	}

	return false
}

// resolveNATTranslation computes the per-server NAT translation for one
// expose entry. Returns nil when expose.NAT is nil.
func resolveNATTranslation(expose *gwapi.PeeringEntryExpose, serverIP netip.Addr, subnetCIDR netip.Prefix) (*TranslatedAddress, error) {
	if expose.NAT == nil {
		return nil, nil //nolint:nilnil // "no NAT" is a legitimate no-op, not an error
	}
	if len(expose.As) == 0 {
		return nil, fmt.Errorf("expose.NAT set but no expose.As CIDR") //nolint:err113
	}
	poolCIDR, err := parseFirstAsCIDR(expose.As)
	if err != nil {
		return nil, err
	}

	switch {
	case expose.NAT.Static != nil:
		// IsServerReachable's API-only endpoints have no SSH-discovered IP;
		// skip the per-server offset in that case and return just the pool
		// so callers still see the peering as ALLOW with NAT configured.
		if !serverIP.IsValid() {
			return &TranslatedAddress{SourcePool: poolCIDR}, nil
		}
		natIP, err := CalculateStaticNATIP(serverIP, subnetCIDR.Masked().Addr(), poolCIDR.Masked().Addr())
		if err != nil {
			return nil, fmt.Errorf("static NAT: %w", err)
		}

		return &TranslatedAddress{
			SourceIP:      natIP,
			SourcePool:    poolCIDR,
			DestinationIP: natIP,
		}, nil

	case expose.NAT.Masquerade != nil:
		// Exact source IP is dataplane-assigned; verify by prefix containment.
		return &TranslatedAddress{
			SourceIP:   poolCIDR.Masked().Addr(),
			SourcePool: poolCIDR,
		}, nil

	case expose.NAT.PortForward != nil:
		poolIP := poolCIDR.Masked().Addr()
		ports := map[ProtoPort]ProtoPort{}
		for _, pf := range expose.NAT.PortForward.Ports {
			extPort, err := parseSinglePort(pf.Port)
			if err != nil {
				return nil, fmt.Errorf("port-forward external port: %w", err)
			}
			intPort, err := parseSinglePort(pf.As)
			if err != nil {
				return nil, fmt.Errorf("port-forward internal port: %w", err)
			}
			proto := string(pf.Protocol)
			if proto == "" {
				proto = "tcp"
			}
			ports[ProtoPort{Protocol: proto, Port: extPort}] = ProtoPort{Protocol: proto, Port: intPort}
		}

		return &TranslatedAddress{
			DestinationIP: poolIP,
			SourceIP:      poolIP,
			SourcePool:    poolCIDR,
			PortForwards:  ports,
		}, nil
	}

	return nil, nil //nolint:nilnil // defensive: validation enforces at least one NAT mode
}

func parseFirstAsCIDR(as []gwapi.PeeringEntryAs) (netip.Prefix, error) {
	for _, a := range as {
		if a.CIDR == "" {
			continue
		}

		cidr, err := netip.ParsePrefix(a.CIDR)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("parsing expose.As CIDR %q: %w", a.CIDR, err)
		}

		return cidr, nil
	}

	return netip.Prefix{}, fmt.Errorf("no CIDR in expose.As") //nolint:err113
}

func parseSinglePort(s string) (uint16, error) {
	if strings.Contains(s, "-") {
		parts := strings.Split(s, "-")
		if len(parts) != 2 || parts[0] != parts[1] {
			return 0, fmt.Errorf("port range %q unsupported in Phase 1", s) //nolint:err113
		}
		s = parts[0]
	}
	var v uint16
	_, err := fmt.Sscanf(s, "%d", &v)
	if err != nil {
		return 0, fmt.Errorf("parsing port %q: %w", s, err)
	}

	return v, nil
}

// ExternalPeeringProvider emits expectations for external reachability. Phase 1
// leaves externals out of the matrix; the curl loop in TestConnectivity keeps
// handling them directly. The provider is registered but currently a no-op so
// the pipeline shape is ready for Phase 2.
type ExternalPeeringProvider struct{}

func (p *ExternalPeeringProvider) Name() string { return "external-peering" }

func (p *ExternalPeeringProvider) BuildExpectations(context.Context, kclient.Reader, []Endpoint, *ConnectivityMatrix) ([]ConnectivityExpectation, error) {
	return nil, nil
}

func listVPCs(ctx context.Context, kube kclient.Reader) (map[string]*vpcapi.VPC, error) {
	list := &vpcapi.VPCList{}
	if err := kube.List(ctx, list, kclient.InNamespace(kmetav1.NamespaceDefault)); err != nil {
		return nil, fmt.Errorf("listing VPCs: %w", err)
	}
	out := map[string]*vpcapi.VPC{}
	for i := range list.Items {
		v := &list.Items[i]
		out[v.Name] = v
	}

	return out, nil
}
