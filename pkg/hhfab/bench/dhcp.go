// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"log/slog"
	"math/bits"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// The DHCP bench owns everything it creates and depends on no other bench
// object but one leaf to relay through. Its VPCs live in their own VLAN and
// IPv4 namespaces, which may overlap the ones bench init creates, so it never
// has to know what else is in the fabric. All of it carries LabelFabric with
// this value, which is how a run finds its own objects and how bench clean
// removes them.
const (
	DHCPLabel     = "bench-dhcp"
	DHCPNamespace = "bench-dhcp"

	// The VLAN range stays below the 3000-3999 the fabric reserves for IRB and
	// the TH5 workaround.
	DHCPVLANFrom = 1000
	DHCPVLANTo   = 2999

	DHCPIPv4Subnet = "10.0.0.0/8"

	// MaxDHCPSubnetsPerVPC is the VPC API's limit.
	MaxDHCPSubnetsPerVPC = 20

	// dhcpVPCPrefix keeps VPC names, which the VPC API caps at 11 characters,
	// at dhcp-NNNN.
	dhcpVPCPrefix = "dhcp-"
	maxDHCPVPCs   = 9999
)

// Defaults for `bench dhcp`: a 20k server fleet in subnets of at most 1000
// servers, so 10 VPCs of 2 subnets of 1000 servers, each subnet well below the
// ~8k leases at which a DHCPSubnet's status nears etcd's request size limit.
const (
	DefaultDHCPServers          = 20_000
	DefaultDHCPSubnets          = 2
	DefaultDHCPVPCs             = 0
	DefaultDHCPMaxSubnetServers = 1000
	DefaultDHCPRounds           = 3
	DefaultDHCPRepeats          = 5
	DefaultDHCPRepeatInterval   = 10 * time.Second
	DefaultDHCPRoundTimeout     = 30 * time.Minute
)

// dhcpObjectsTimeout bounds each wait on the controllers during cleanup: the
// VPCs going away with their DHCPSubnets, and coming back.
const dhcpObjectsTimeout = 5 * time.Minute

// DHCPLayout is the requested fleet: so many servers in total, spread evenly
// over VPCs of so many subnets each. With VPCs left at 0 there are just enough
// VPCs for at most MaxSubnetServers servers in each subnet.
type DHCPLayout struct {
	Servers          int
	Subnets          int
	VPCs             int
	MaxSubnetServers int
}

// vpcs is how many VPCs the fleet is spread over.
func (l DHCPLayout) vpcs() int {
	if l.VPCs > 0 || l.Subnets < 1 || l.MaxSubnetServers < 1 {
		return l.VPCs
	}

	perVPC := l.Subnets * l.MaxSubnetServers

	return (l.Servers + perVPC - 1) / perVPC
}

// dhcpSubnet is one bench DHCP subnet: where it sits, and once dhcpd's
// DHCPSubnet exists, what dhcpd keys it on.
type dhcpSubnet struct {
	index   int // global, across VPCs
	vpc     string
	name    string
	vlan    uint16
	prefix  netip.Prefix
	gateway netip.Addr
	start   netip.Addr
	end     netip.Addr

	// Its servers are numbered first to first+servers-1 across the fleet.
	first   int
	servers int

	// From the DHCPSubnet the VPC controller creates, so the relay sends what
	// dhcpd looks the subnet up by rather than a guess at it.
	vrf       string
	circuitID string
}

// dhcpSubnetName is the DHCPSubnet the VPC controller creates for a subnet.
func (s *dhcpSubnet) dhcpSubnetName() string {
	return s.vpc + "--" + s.name
}

// usable reports whether dhcpd may hand ip out in this subnet: inside the DHCP
// range, which already leaves out the network, gateway and broadcast addresses.
func (s *dhcpSubnet) usable(ip netip.Addr) bool {
	return ip.Compare(s.start) >= 0 && ip.Compare(s.end) <= 0
}

// planDHCP lays the fleet out: VPC and subnet names, VLANs, and CIDRs carved in
// order from DHCPIPv4Subnet. The servers are spread evenly over the subnets,
// the first ones taking one more when they don't divide; every subnet gets the
// same prefix, with room for the most servers any of them has plus the
// network, gateway and broadcast addresses and the readiness probe, whose
// DISCOVER holds an address for a while without ever being requested.
func planDHCP(layout DHCPLayout) ([]*dhcpSubnet, error) {
	vpcs := layout.vpcs()

	switch {
	case layout.Servers < 1:
		return nil, fmt.Errorf("servers must be at least 1, got %d", layout.Servers) //nolint:err113
	case layout.Servers > maxDHCPServers:
		return nil, fmt.Errorf("%d servers exceed the %d the bench MAC scheme can number", layout.Servers, maxDHCPServers) //nolint:err113
	case layout.Subnets < 1 || layout.Subnets > MaxDHCPSubnetsPerVPC:
		return nil, fmt.Errorf("subnets per vpc must be between 1 and %d, got %d", MaxDHCPSubnetsPerVPC, layout.Subnets) //nolint:err113
	case layout.VPCs < 0:
		return nil, fmt.Errorf("vpcs must not be negative, got %d", layout.VPCs) //nolint:err113
	case layout.VPCs == 0 && layout.MaxSubnetServers < 1:
		return nil, fmt.Errorf("max servers per subnet must be at least 1 to pick the vpcs, got %d", layout.MaxSubnetServers) //nolint:err113
	case vpcs > maxDHCPVPCs:
		return nil, fmt.Errorf("%d vpcs exceed the %d the bench can name", vpcs, maxDHCPVPCs) //nolint:err113
	}

	total := vpcs * layout.Subnets
	if total > layout.Servers {
		return nil, fmt.Errorf("%d servers can't fill %d vpcs of %d subnets, every subnet needs at least one", //nolint:err113
			layout.Servers, vpcs, layout.Subnets)
	}
	if vlans := DHCPVLANTo - DHCPVLANFrom + 1; total > vlans {
		return nil, fmt.Errorf("%d subnets need %d VLANs, the bench DHCP VLAN range %d-%d has %d", //nolint:err113
			total, total, DHCPVLANFrom, DHCPVLANTo, vlans)
	}

	perSubnet, extra := layout.Servers/total, layout.Servers%total
	most := perSubnet
	if extra > 0 {
		most++
	}

	// Network, gateway, broadcast and the probe's address.
	need := uint64(most) + 4               //nolint:gosec // at least 1, checked above
	hostBits := max(bits.Len64(need-1), 2) // the VPC API takes nothing longer than /30

	base := netip.MustParsePrefix(DHCPIPv4Subnet)
	if hostBits > 32-base.Bits() || uint64(total)<<hostBits > uint64(1)<<(32-base.Bits()) { //nolint:gosec // bounded above
		return nil, fmt.Errorf("%d subnets of %d servers (/%d each) do not fit in %s", //nolint:err113
			total, most, 32-hostBits, DHCPIPv4Subnet)
	}

	out := make([]*dhcpSubnet, 0, total)
	first := 0
	for vpcIdx := range vpcs {
		for subIdx := range layout.Subnets {
			idx := vpcIdx*layout.Subnets + subIdx

			servers := perSubnet
			if idx < extra {
				servers++
			}

			network, err := addrAdd(base.Addr(), uint64(idx)<<hostBits) //nolint:gosec // idx is non-negative
			if err != nil {
				return nil, fmt.Errorf("allocating subnet %d: %w", idx+1, err)
			}

			prefix := netip.PrefixFrom(network, 32-hostBits)
			gateway := network.Next()

			broadcast, err := addrAdd(network, uint64(1)<<hostBits-1)
			if err != nil {
				return nil, fmt.Errorf("allocating subnet %d: %w", idx+1, err)
			}

			out = append(out, &dhcpSubnet{
				index:   idx,
				vpc:     dhcpVPCName(vpcIdx),
				name:    fmt.Sprintf("s%02d", subIdx+1),
				vlan:    uint16(DHCPVLANFrom + idx), //nolint:gosec // checked against the VLAN range
				prefix:  prefix,
				gateway: gateway,
				start:   gateway.Next(),
				end:     broadcast.Prev(),
				first:   first,
				servers: servers,
			})
			first += servers
		}
	}

	return out, nil
}

func dhcpVPCName(idx int) string {
	return fmt.Sprintf("%s%04d", dhcpVPCPrefix, idx+1)
}

// dhcpServersPerSubnet shows how many servers each subnet has: one number, or
// the range when they don't divide evenly.
func dhcpServersPerSubnet(plan []*dhcpSubnet) string {
	least, most := plan[0].servers, plan[0].servers
	for _, sub := range plan {
		least, most = min(least, sub.servers), max(most, sub.servers)
	}

	if least == most {
		return fmt.Sprint(most)
	}

	return fmt.Sprintf("%d-%d", least, most)
}

// dhcpObjects builds the namespaces and VPCs for a plan. pxeURL, when set,
// goes on every subnet, so dhcpd answers PXE clients with boot information
// instead of logging that PXE isn't configured for each of them.
func dhcpObjects(plan []*dhcpSubnet, pxeURL string) []kclient.Object {
	labels := map[string]string{LabelFabric: DHCPLabel}

	objs := []kclient.Object{
		&wiringapi.VLANNamespace{
			TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindVLANNamespace, APIVersion: wiringapi.GroupVersion.String()},
			ObjectMeta: objMeta(DHCPNamespace, labels),
			Spec: wiringapi.VLANNamespaceSpec{
				Ranges: []meta.VLANRange{{From: DHCPVLANFrom, To: DHCPVLANTo}},
			},
		},
		&vpcapi.IPv4Namespace{
			TypeMeta:   kmetav1.TypeMeta{Kind: vpcapi.KindIPv4Namespace, APIVersion: vpcapi.GroupVersion.String()},
			ObjectMeta: objMeta(DHCPNamespace, labels),
			Spec: vpcapi.IPv4NamespaceSpec{
				Topology: vpcapi.IPv4NamespaceTopology{Fabric: wiringapi.DefaultFabric},
				Subnets:  []string{DHCPIPv4Subnet},
			},
		},
	}

	var vpc *vpcapi.VPC
	for _, sub := range plan {
		if vpc == nil || vpc.Name != sub.vpc {
			vpc = &vpcapi.VPC{
				TypeMeta:   kmetav1.TypeMeta{Kind: vpcapi.KindVPC, APIVersion: vpcapi.GroupVersion.String()},
				ObjectMeta: objMeta(sub.vpc, labels),
				Spec: vpcapi.VPCSpec{
					Mode:          vpcapi.VPCModeL3VNI,
					Topology:      vpcapi.VPCTopology{Fabric: wiringapi.DefaultFabric, Domains: []string{wiringapi.DefaultFabricDomain}},
					IPv4Namespace: DHCPNamespace,
					VLANNamespace: DHCPNamespace,
					Subnets:       map[string]*vpcapi.VPCSubnet{},
				},
			}
			objs = append(objs, vpc)
		}

		subnet := &vpcapi.VPCSubnet{
			Subnet:  sub.prefix.String(),
			Gateway: sub.gateway.String(),
			VLAN:    sub.vlan,
			DHCP: vpcapi.VPCDHCP{
				Enable: true,
				Range:  &vpcapi.VPCDHCPRange{Start: sub.start.String(), End: sub.end.String()},
			},
		}
		if pxeURL != "" {
			// Setting any option means setting the lease too; it stays at the
			// controller's default.
			subnet.DHCP.Options = &vpcapi.VPCDHCPOptions{PXEURL: pxeURL, LeaseTimeSeconds: dhcpDefaultLeaseSeconds}
		}

		vpc.Spec.Subnets[sub.name] = subnet
	}

	return objs
}

// dhcpDefaultLeaseSeconds is the VPC controller's DefaultLeaseTime, which the
// bench leaves in place.
const dhcpDefaultLeaseSeconds = 3600

// DHCPRelay is the leaf the bench relays through: dhcpd only accepts relayed
// requests whose giaddr is a leaf's management IP.
type DHCPRelay struct {
	Name string
	IP   netip.Prefix
}

// FindDHCPRelay picks the relay leaf: the named one, or the first bench leaf by
// name that has a management IP.
func FindDHCPRelay(ctx context.Context, kube kclient.Reader, name string) (DHCPRelay, error) {
	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches, kclient.HasLabels{LabelFabric}); err != nil {
		return DHCPRelay{}, fmt.Errorf("listing switches: %w", err)
	}

	slices.SortFunc(switches.Items, func(a, b wiringapi.Switch) int { return strings.Compare(a.Name, b.Name) })

	for _, sw := range switches.Items {
		if name != "" && sw.Name != name {
			continue
		}
		if !sw.Spec.Role.IsLeaf() || sw.Spec.IP == "" {
			if name != "" {
				return DHCPRelay{}, fmt.Errorf("switch %s is not a leaf with a management IP, dhcpd only accepts relays that are", name) //nolint:err113
			}

			continue
		}

		ip, err := netip.ParsePrefix(sw.Spec.IP)
		if err != nil {
			return DHCPRelay{}, fmt.Errorf("parsing management IP %q of %s: %w", sw.Spec.IP, sw.Name, err)
		}

		return DHCPRelay{Name: sw.Name, IP: ip}, nil
	}

	if name != "" {
		return DHCPRelay{}, fmt.Errorf("no bench switch %s", name) //nolint:err113
	}

	return DHCPRelay{}, fmt.Errorf("no bench leaf with a management IP to relay through, run bench init first") //nolint:err113
}

// resetDHCPObjects gives a round an empty dhcpd: it deletes the bench DHCP
// VPCs, waits until their DHCPSubnets are gone, applies the namespaces and VPCs
// again, waits for the controller to create every DHCPSubnet, and reads the
// VRF and circuit ID dhcpd will look each subnet up by.
func resetDHCPObjects(ctx context.Context, kube kclient.Client, plan []*dhcpSubnet, objs []kclient.Object) error {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs, kclient.MatchingLabels{LabelFabric: DHCPLabel}); err != nil {
		return fmt.Errorf("listing bench DHCP VPCs: %w", err)
	}

	for idx := range vpcs.Items {
		if err := kube.Delete(ctx, &vpcs.Items[idx]); kclient.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting VPC %s: %w", vpcs.Items[idx].Name, err)
		}
	}

	if err := waitDHCPGone(ctx, kube); err != nil {
		return err
	}

	for _, obj := range objs {
		if _, err := applyOne(ctx, kube, obj); err != nil {
			return fmt.Errorf("applying %s %s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
		}
	}

	return waitDHCPSubnets(ctx, kube, plan)
}

// waitDHCPGone waits until no bench DHCP VPC and no DHCPSubnet of one is left.
// A VPC being deleted can't be recreated under its name, and the DHCPSubnets,
// which it owns, only go once the garbage collector gets to them.
func waitDHCPGone(ctx context.Context, kube kclient.Client) error {
	return pollUntil(ctx, dhcpObjectsTimeout, "the old bench DHCP VPCs and subnets to go away", func(ctx context.Context) (bool, error) {
		vpcs := &vpcapi.VPCList{}
		if err := kube.List(ctx, vpcs, kclient.MatchingLabels{LabelFabric: DHCPLabel}); err != nil {
			return false, fmt.Errorf("listing bench DHCP VPCs: %w", err)
		}
		if len(vpcs.Items) > 0 {
			return false, nil
		}

		subnets := &dhcpapi.DHCPSubnetList{}
		if err := kube.List(ctx, subnets); err != nil {
			return false, fmt.Errorf("listing DHCP subnets: %w", err)
		}

		for _, sub := range subnets.Items {
			if strings.HasPrefix(sub.Name, dhcpVPCPrefix) && strings.Contains(sub.Name, "--") {
				return false, nil
			}
		}

		return true, nil
	})
}

// waitDHCPSubnets waits until the controller has created every DHCPSubnet of
// the plan and fills in what dhcpd keys each one on.
func waitDHCPSubnets(ctx context.Context, kube kclient.Client, plan []*dhcpSubnet) error {
	return pollUntil(ctx, dhcpObjectsTimeout, "the bench DHCP subnets to be created", func(ctx context.Context) (bool, error) {
		for _, sub := range plan {
			if sub.vrf != "" {
				continue
			}

			obj := &dhcpapi.DHCPSubnet{}
			if err := kube.Get(ctx, kclient.ObjectKey{Name: sub.dhcpSubnetName(), Namespace: kmetav1.NamespaceDefault}, obj); err != nil {
				if kapierrors.IsNotFound(err) {
					return false, nil
				}

				return false, fmt.Errorf("getting DHCP subnet %s: %w", sub.dhcpSubnetName(), err)
			}

			if obj.Spec.VRF == "" || obj.Spec.CircuitID == "" {
				return false, nil
			}

			sub.vrf, sub.circuitID = obj.Spec.VRF, obj.Spec.CircuitID
		}

		return true, nil
	})
}

// clearDHCPKeys forgets what dhcpd keyed the subnets on, so the next round
// reads it again from the recreated DHCPSubnets.
func clearDHCPKeys(plan []*dhcpSubnet) {
	for _, sub := range plan {
		sub.vrf, sub.circuitID = "", ""
	}
}

// pollUntil calls check every second until it reports done, the apiserver
// rejects a request, or timeout passes. Any other error, such as the apiserver
// restarting after an etcd stall, is waited out: it is logged when it starts
// and when it clears, and polling goes on. check gets a context that ends with
// the timeout, so a request that hangs can't outlast it.
func pollUntil(ctx context.Context, timeout time.Duration, what string, check func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error

	for {
		done, err := check(ctx)
		switch {
		case err != nil && isRejection(err):
			return err
		case err != nil:
			if lastErr == nil {
				slog.Warn("Retrying while waiting for "+what, "err", err)
			}
			lastErr = err
		case lastErr != nil:
			slog.Info("Recovered while waiting for " + what)
			lastErr = nil
		}

		if done && err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("waiting for %s: %w: %w", what, ctx.Err(), lastErr)
			}

			return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// storedLeases reads every bench DHCPSubnet's allocations, keyed by subnet
// index, for the checks against dhcpd's own record of the round.
func storedLeases(ctx context.Context, kube kclient.Reader, plan []*dhcpSubnet) (map[int]map[string]dhcpapi.DHCPAllocated, error) {
	out := make(map[int]map[string]dhcpapi.DHCPAllocated, len(plan))

	for _, sub := range plan {
		obj := &dhcpapi.DHCPSubnet{}
		if err := kube.Get(ctx, kclient.ObjectKey{Name: sub.dhcpSubnetName(), Namespace: kmetav1.NamespaceDefault}, obj); err != nil {
			return nil, fmt.Errorf("getting DHCP subnet %s: %w", sub.dhcpSubnetName(), err)
		}

		out[sub.index] = obj.Status.Allocated
	}

	return out, nil
}

// dhcpServerMAC numbers servers under a locally administered prefix of their
// own, apart from the switches' 02:b0:00 and the VLAB's 0c:20:12.
func dhcpServerMAC(idx int) net.HardwareAddr {
	return net.HardwareAddr{0x02, 0xdc, 0x00, byte(idx >> 16), byte(idx >> 8), byte(idx)}
}

// dhcpProbeMAC is the readiness probe's MAC for a subnet, apart from every
// server's so its leftover offer is never mistaken for one.
func dhcpProbeMAC(subnetIdx int) net.HardwareAddr {
	return net.HardwareAddr{0x02, 0xdc, 0x01, byte(subnetIdx >> 16), byte(subnetIdx >> 8), byte(subnetIdx)}
}

const maxDHCPServers = 1 << 24

func dhcpHostname(sub *dhcpSubnet, idx int) string {
	return fmt.Sprintf("%s-%s-%05d", sub.vpc, sub.name, idx+1)
}
