// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math/bits"
	"net/netip"
	"regexp"
	"slices"
	"strconv"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LabelFabric marks every object the bench creates with the fabric it belongs
// to, so listing, health and cleanup can be scoped without name matching.
const LabelFabric = "bench.githedgehog.com/fabric"

// Each fabric gets its own VLANNamespace and IPv4Namespace with identical
// contents. Neither VLANNamespace.Validate nor IPv4Namespace.Validate compares
// against other namespaces - they only check within-namespace overlap and the
// Fabric reserved ranges - so fabrics can reuse the same VLANs and subnets,
// which is what makes them genuinely independent.
const (
	// VLANFrom/VLANTo avoid VPCIRBVLANs (3000-3899) and TH5WorkaroundVLANs
	// (3900-3999), which VLANNamespace.Validate rejects overlapping.
	VLANFrom = 1000
	VLANTo   = 2999

	// IPv4Subnet is disjoint from every Fabric reserved subnet, all of which
	// live under 172.30/16.
	IPv4Subnet = "10.0.0.0/8"
)

// dataPortRe matches a non-management data port name, e.g. "E1/33".
var dataPortRe = regexp.MustCompile(`^E(\d+)/(\d+)$`)

// Generator turns a list of FabricSpecs into wiring and VPC objects.
type Generator struct {
	specs    []FabricSpec
	alloc    *Allocator
	profiles map[string]*wiringapi.SwitchProfile
}

// NewGenerator validates each spec against its switch profile and returns a
// generator ready to emit objects.
func NewGenerator(specs []FabricSpec, alloc *Allocator, profiles map[string]*wiringapi.SwitchProfile) (*Generator, error) {
	g := &Generator{specs: specs, alloc: alloc, profiles: profiles}

	for _, spec := range specs {
		if _, err := g.plan(spec); err != nil {
			return nil, fmt.Errorf("fabric %q: %w", spec.Name, err)
		}
	}

	return g, nil
}

// portPlan is the per-switch port assignment for one fabric, derived from the
// switch profile rather than hardcoded for any particular model.
type portPlan struct {
	profile *wiringapi.SwitchProfile

	spinePorts  []string // ports a spine uses for leaves, in order
	uplinkPorts []string // ports a leaf uses for spines, in order
	serverPorts []string // physical leaf ports broken out for servers

	subports uint // server subports per physical port
}

// plan computes and validates the port assignment for a fabric.
func (g *Generator) plan(spec FabricSpec) (*portPlan, error) {
	profile, ok := g.profiles[spec.Profile]
	if !ok || profile == nil {
		return nil, fmt.Errorf("unknown switch profile %q", spec.Profile) //nolint:err113
	}

	ports := dataPorts(profile)

	spineNeed := spec.Leaves * spec.FabricLinks
	if uint(len(ports)) < spineNeed {
		return nil, fmt.Errorf("spine needs %d ports (%d leaves x %d links) but profile %q has %d data ports", //nolint:err113
			spineNeed, spec.Leaves, spec.FabricLinks, spec.Profile, len(ports))
	}

	uplinkNeed := spec.Spines * spec.FabricLinks
	leafNeed := uplinkNeed + spec.ServerPorts
	if uint(len(ports)) < leafNeed {
		return nil, fmt.Errorf("leaf needs %d ports (%d uplinks + %d server ports) but profile %q has %d data ports", //nolint:err113
			leafNeed, uplinkNeed, spec.ServerPorts, spec.Profile, len(ports))
	}

	plan := &portPlan{
		profile:     profile,
		spinePorts:  ports[:spineNeed],
		uplinkPorts: ports[:uplinkNeed],
		serverPorts: ports[uplinkNeed:leafNeed],
		subports:    spec.ServerSubportsPerPort(),
	}

	// Every server-facing port has to actually support the requested breakout.
	for _, port := range plan.serverPorts {
		got, err := breakoutSubports(profile, port, spec.ServerBreakout)
		if err != nil {
			return nil, err
		}
		if got != plan.subports {
			return nil, fmt.Errorf("port %q breakout %q yields %d subports, expected %d", //nolint:err113
				port, spec.ServerBreakout, got, plan.subports)
		}
	}

	if err := checkPortCapacity(profile, spec, plan); err != nil {
		return nil, err
	}

	return plan, nil
}

// dataPorts returns the profile's non-management ports, ordered by ASIC and
// port number so that assignment is deterministic.
func dataPorts(profile *wiringapi.SwitchProfile) []string {
	type parsed struct {
		name       string
		asic, port int
	}

	found := make([]parsed, 0, len(profile.Spec.Ports))
	for name, port := range profile.Spec.Ports {
		if port.Management {
			continue
		}

		m := dataPortRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		asic, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		num, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}

		found = append(found, parsed{name: name, asic: asic, port: num})
	}

	slices.SortFunc(found, func(a, b parsed) int {
		if c := cmp.Compare(a.asic, b.asic); c != 0 {
			return c
		}

		return cmp.Compare(a.port, b.port)
	})

	out := make([]string, 0, len(found))
	for _, p := range found {
		out = append(out, p.name)
	}

	return out
}

// breakoutSubports returns how many subports the given breakout mode yields on
// a port, erroring if the port's profile does not support the mode.
func breakoutSubports(profile *wiringapi.SwitchProfile, port, mode string) (uint, error) {
	p, ok := profile.Spec.Ports[port]
	if !ok {
		return 0, fmt.Errorf("profile %q has no port %q", profile.Name, port) //nolint:err113
	}
	if p.Profile == "" {
		return 0, fmt.Errorf("port %q has no port profile, cannot be broken out", port) //nolint:err113
	}

	breakout := profile.Spec.PortProfiles[p.Profile].Breakout
	if breakout == nil {
		return 0, fmt.Errorf("port %q profile %q does not support breakouts", port, p.Profile) //nolint:err113
	}

	supported, ok := breakout.Supported[mode]
	if !ok {
		modes := make([]string, 0, len(breakout.Supported))
		for m := range breakout.Supported {
			modes = append(modes, m)
		}
		slices.Sort(modes)

		return 0, fmt.Errorf("port %q profile %q does not support breakout %q, supported: %v", //nolint:err113
			port, p.Profile, mode, modes)
	}

	return uint(len(supported.Offsets)), nil
}

// apiPortName returns the API name of a subport. A breakout-capable port
// exposes both "E1/33" and "E1/33/1" for the same NOS port, so the subport form
// is always used - including for single-subport modes like 1x800G - to keep
// every port name unambiguous and to match what GetNOS2APIPortsFor returns.
// Ports with no breakout profile, such as the DS5000's SFP28 ports, have only
// the plain form.
func apiPortName(profile *wiringapi.SwitchProfile, port string, sub uint) string {
	p, ok := profile.Spec.Ports[port]
	if !ok || p.Profile == "" || profile.Spec.PortProfiles[p.Profile].Breakout == nil {
		return port
	}

	return fmt.Sprintf("%s/%d", port, sub+1)
}

// effectiveSubports is how many subports a port yields given the breakouts the
// bench configures, falling back to the profile default.
func effectiveSubports(profile *wiringapi.SwitchProfile, port string, breakouts map[string]string) uint {
	p, ok := profile.Spec.Ports[port]
	if !ok || p.Profile == "" {
		return 1
	}

	breakout := profile.Spec.PortProfiles[p.Profile].Breakout
	if breakout == nil {
		return 1
	}

	mode, ok := breakouts[port]
	if !ok {
		mode = breakout.Default
	}

	if supported, ok := breakout.Supported[mode]; ok {
		return uint(len(supported.Offsets))
	}

	return 1
}

// checkPortCapacity verifies the leaf configuration against the profile's
// per-pipeline and switch-wide port limits, so an oversized request fails here
// rather than in the admission webhook partway through a long run.
func checkPortCapacity(profile *wiringapi.SwitchProfile, spec FabricSpec, plan *portPlan) error {
	breakouts := map[string]string{}
	for _, port := range plan.serverPorts {
		breakouts[port] = spec.ServerBreakout
	}

	total := uint(0)
	perPipeline := map[string]uint{}

	for name, port := range profile.Spec.Ports {
		if port.Management {
			continue
		}

		subports := effectiveSubports(profile, name, breakouts)
		total += subports

		if port.Pipeline != "" {
			perPipeline[port.Pipeline] += subports
		}
	}

	if profile.Spec.MaxPorts > 0 && total > uint(profile.Spec.MaxPorts) {
		return fmt.Errorf("leaf configuration needs %d subports, profile %q allows %d", //nolint:err113
			total, profile.Name, profile.Spec.MaxPorts)
	}

	for name, used := range perPipeline {
		pipeline, ok := profile.Spec.Pipelines[name]
		if !ok || pipeline.MaxPorts == 0 {
			continue
		}

		if used > uint(pipeline.MaxPorts) {
			return fmt.Errorf("pipeline %q would carry %d subports, profile %q allows %d per pipeline", //nolint:err113
				name, used, profile.Name, pipeline.MaxPorts)
		}
	}

	return nil
}

// domainGen is one domain of a fabric being generated: a full copy of the
// fabric's shape, and everything that differs between the copies.
type domainGen struct {
	spec   FabricSpec
	plan   *portPlan
	labels map[string]string

	idx      uint   // within the fabric
	name     string // as the Fabric declares it
	slot     uint   // the fabric's, which keys its leaf ASNs
	unit     uint   // which keys its addresses
	spineASN uint32

	// VPCs are numbered and addressed across the whole fabric, since they
	// share its namespaces; this domain's are a contiguous run of them.
	firstVPC uint
	subnets  []netip.Prefix
}

// Names of the objects a domain owns. A single domain adds nothing to them,
// so a one-domain fabric keeps the names it always had; with more, switches
// and servers carry a "-dN" so that the copies do not collide. VPCs never do:
// the fabric name is capped at 7 characters so that "<fabric>-NNN" fits the 11
// character VPC name limit, which leaves no room, so they are numbered across
// the fabric instead.

func (d *domainGen) prefix() string {
	if d.spec.Domains == 1 {
		return d.spec.Name
	}

	return fmt.Sprintf("%s-d%d", d.spec.Name, d.idx+1)
}

func (d *domainGen) spineName(idx uint) string {
	return fmt.Sprintf("%s-spine-%02d", d.prefix(), idx+1)
}

func (d *domainGen) leafName(idx uint) string {
	return fmt.Sprintf("%s-leaf-%02d", d.prefix(), idx+1)
}

func (d *domainGen) serverName(idx uint) string {
	return fmt.Sprintf("%s-srv-%05d", d.prefix(), idx+1)
}

func (d *domainGen) vpcName(idx uint) string {
	return fmt.Sprintf("%s-%03d", d.spec.Name, d.firstVPC+idx+1)
}

// Generate emits every object for every fabric into the loader.
func (g *Generator) Generate(ctx context.Context, l *apiutil.Loader) error {
	for slot, spec := range g.specs {
		if err := g.generateFabric(ctx, l, uint(slot), spec); err != nil { //nolint:gosec // slot is bounded by len(specs)
			return fmt.Errorf("fabric %q: %w", spec.Name, err)
		}
	}

	return nil
}

func (g *Generator) generateFabric(ctx context.Context, l *apiutil.Loader, slot uint, spec FabricSpec) error {
	plan, err := g.plan(spec)
	if err != nil {
		return err
	}

	labels := map[string]string{LabelFabric: spec.Name}

	asns, err := g.alloc.ASNs(slot)
	if err != nil {
		return err
	}

	domains := make(map[string]wiringapi.FabricDomainSpec, spec.Domains)
	for idx := range spec.Domains {
		domains[spec.DomainName(idx)] = wiringapi.FabricDomainSpec{SpineASN: asns.Spines[idx], GatewayASN: asns.Gateways[idx]}
	}

	if err := l.Add(ctx, &wiringapi.Fabric{
		TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindFabric, APIVersion: wiringapi.GroupVersion.String()},
		ObjectMeta: objMeta(spec.Name, labels),
		Spec: wiringapi.FabricSpec{
			LeafASNStart: asns.LeafStart,
			LeafASNEnd:   asns.LeafEnd,
			Domains:      domains,
		},
	}); err != nil {
		return fmt.Errorf("adding fabric: %w", err)
	}

	if err := l.Add(ctx, &wiringapi.VLANNamespace{
		TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindVLANNamespace, APIVersion: wiringapi.GroupVersion.String()},
		ObjectMeta: objMeta(spec.Name, labels),
		Spec: wiringapi.VLANNamespaceSpec{
			Ranges: []meta.VLANRange{{From: VLANFrom, To: VLANTo}},
		},
	}); err != nil {
		return fmt.Errorf("adding VLAN namespace: %w", err)
	}

	if err := l.Add(ctx, &vpcapi.IPv4Namespace{
		TypeMeta:   kmetav1.TypeMeta{Kind: vpcapi.KindIPv4Namespace, APIVersion: vpcapi.GroupVersion.String()},
		ObjectMeta: objMeta(spec.Name, labels),
		Spec: vpcapi.IPv4NamespaceSpec{
			Topology: vpcapi.IPv4NamespaceTopology{Fabric: spec.Name},
			Subnets:  []string{IPv4Subnet},
		},
	}); err != nil {
		return fmt.Errorf("adding IPv4 namespace: %w", err)
	}

	if err := l.Add(ctx, &wiringapi.SwitchGroup{
		TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindSwitchGroup, APIVersion: wiringapi.GroupVersion.String()},
		ObjectMeta: objMeta(spec.Name, labels),
		Spec: wiringapi.SwitchGroupSpec{
			Topology: wiringapi.SwitchGroupTopology{Fabric: spec.Name},
		},
	}); err != nil {
		return fmt.Errorf("adding switch group: %w", err)
	}

	var subnets []netip.Prefix
	if spec.VPCs > 0 {
		if subnets, err = vpcSubnets(spec); err != nil {
			return err
		}
	}

	for idx := range spec.Domains {
		unit, err := g.alloc.Unit(slot, idx)
		if err != nil {
			return err
		}

		d := &domainGen{
			spec:     spec,
			plan:     plan,
			labels:   labels,
			idx:      idx,
			name:     spec.DomainName(idx),
			slot:     slot,
			unit:     unit,
			spineASN: asns.Spines[idx],
			firstVPC: idx * spec.VPCs,
		}
		if spec.VPCs > 0 {
			d.subnets = subnets[d.firstVPC : d.firstVPC+spec.VPCs]
		}

		if err := g.generateDomain(ctx, l, d); err != nil {
			return fmt.Errorf("domain %s: %w", d.name, err)
		}
	}

	return nil
}

// generateDomain emits one copy of the fabric's shape. Nothing in it refers to
// another domain: its leaves uplink only to its own spines, its servers attach
// only to its own VPCs, and its VPCs peer only with each other.
func (g *Generator) generateDomain(ctx context.Context, l *apiutil.Loader, d *domainGen) error {
	if err := g.generateSwitches(ctx, l, d); err != nil {
		return err
	}
	if err := g.generateFabricConns(ctx, l, d); err != nil {
		return err
	}
	if err := g.generateServers(ctx, l, d); err != nil {
		return err
	}

	return g.generateVPCs(ctx, l, d)
}

// switchTopology places a switch in its domain. It is built per switch so that
// no two objects share the Domains slice, which Default sorts in place.
func (d *domainGen) switchTopology() wiringapi.SwitchTopology {
	return wiringapi.SwitchTopology{Fabric: d.spec.Name, Domains: []string{d.name}}
}

func (g *Generator) generateSwitches(ctx context.Context, l *apiutil.Loader, d *domainGen) error {
	spec, plan, labels := d.spec, d.plan, d.labels

	// Spines occupy switch indices [0, Spines), leaves [Spines, Switches).
	for idx := range spec.Spines {
		mgmt, err := g.alloc.SwitchMgmtIP(d.unit, idx)
		if err != nil {
			return err
		}
		proto, err := g.alloc.SwitchProtocolIP(d.unit, idx)
		if err != nil {
			return err
		}

		if err := l.Add(ctx, &wiringapi.Switch{
			TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindSwitch, APIVersion: wiringapi.GroupVersion.String()},
			ObjectMeta: objMeta(d.spineName(idx), labels),
			Spec: wiringapi.SwitchSpec{
				Role:           wiringapi.SwitchRoleSpine,
				Description:    fmt.Sprintf("bench %s spine %d", d.prefix(), idx+1),
				Profile:        spec.Profile,
				Topology:       d.switchTopology(),
				Groups:         []string{spec.Name},
				VLANNamespaces: []string{spec.Name},
				ASN:            d.spineASN,
				IP:             mgmt.String(),
				ProtocolIP:     proto.String(),
			},
		}); err != nil {
			return fmt.Errorf("adding spine %d: %w", idx+1, err)
		}
	}

	breakouts := map[string]string{}
	for _, port := range plan.serverPorts {
		breakouts[port] = spec.ServerBreakout
	}

	for idx := range spec.Leaves {
		switchIdx := spec.Spines + idx

		mgmt, err := g.alloc.SwitchMgmtIP(d.unit, switchIdx)
		if err != nil {
			return err
		}
		proto, err := g.alloc.SwitchProtocolIP(d.unit, switchIdx)
		if err != nil {
			return err
		}
		vtep, err := g.alloc.LeafVTEPIP(d.unit, idx)
		if err != nil {
			return err
		}
		asn, err := g.alloc.LeafASN(d.slot, d.idx, idx)
		if err != nil {
			return err
		}

		if err := l.Add(ctx, &wiringapi.Switch{
			TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindSwitch, APIVersion: wiringapi.GroupVersion.String()},
			ObjectMeta: objMeta(d.leafName(idx), labels),
			Spec: wiringapi.SwitchSpec{
				Role:           wiringapi.SwitchRoleServerLeaf,
				Description:    fmt.Sprintf("bench %s leaf %d", d.prefix(), idx+1),
				Profile:        spec.Profile,
				Topology:       d.switchTopology(),
				Groups:         []string{spec.Name},
				VLANNamespaces: []string{spec.Name},
				ASN:            asn,
				IP:             mgmt.String(),
				ProtocolIP:     proto.String(),
				VTEPIP:         vtep.String(),
				PortBreakouts:  copyLabels(breakouts),
			},
		}); err != nil {
			return fmt.Errorf("adding leaf %d: %w", idx+1, err)
		}
	}

	return nil
}

func (g *Generator) generateFabricConns(ctx context.Context, l *apiutil.Loader, d *domainGen) error {
	spec, plan, labels := d.spec, d.plan, d.labels

	// One Connection per spine-leaf pair, carrying FabricLinks links. Link
	// indices are assigned so that they are a pure function of the pair.
	for leafIdx := range spec.Leaves {
		for spineIdx := range spec.Spines {
			links := make([]wiringapi.FabricLink, 0, spec.FabricLinks)

			for linkNum := range spec.FabricLinks {
				spinePort := fmt.Sprintf("%s/%s", d.spineName(spineIdx), apiPortName(plan.profile, plan.spinePorts[leafIdx*spec.FabricLinks+linkNum], 0))
				leafPort := fmt.Sprintf("%s/%s", d.leafName(leafIdx), apiPortName(plan.profile, plan.uplinkPorts[spineIdx*spec.FabricLinks+linkNum], 0))

				link := wiringapi.FabricLink{
					Spine: wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: spinePort}},
					Leaf:  wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: leafPort}},
				}

				if !spec.FabricUnnum {
					linkIdx := (leafIdx*spec.Spines+spineIdx)*spec.FabricLinks + linkNum

					spineIP, leafIP, err := g.alloc.FabricLinkIPs(d.unit, linkIdx)
					if err != nil {
						return err
					}

					link.Spine.IP = spineIP.String()
					link.Leaf.IP = leafIP.String()
				}

				links = append(links, link)
			}

			if err := g.addConn(ctx, l, spec.Name, labels, wiringapi.ConnectionSpec{
				Fabric: &wiringapi.ConnFabric{Links: links},
			}); err != nil {
				return fmt.Errorf("adding fabric connection spine %d leaf %d: %w", spineIdx+1, leafIdx+1, err)
			}
		}
	}

	return nil
}

func (g *Generator) generateServers(ctx context.Context, l *apiutil.Loader, d *domainGen) error {
	spec, plan, labels := d.spec, d.plan, d.labels
	perLeaf := spec.ServersPerLeaf()

	for leafIdx := range spec.Leaves {
		leaf := d.leafName(leafIdx)

		for portIdx, port := range plan.serverPorts {
			for sub := range plan.subports {
				serverIdx := leafIdx*perLeaf + uint(portIdx)*plan.subports + sub //nolint:gosec // portIdx is bounded by ServerPorts
				server := d.serverName(serverIdx)

				if err := l.Add(ctx, &wiringapi.Server{
					TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindServer, APIVersion: wiringapi.GroupVersion.String()},
					ObjectMeta: objMeta(server, labels),
					Spec: wiringapi.ServerSpec{
						Description: fmt.Sprintf("bench %s server %d", d.prefix(), serverIdx+1),
					},
				}); err != nil {
					return fmt.Errorf("adding server %d: %w", serverIdx+1, err)
				}

				switchPort := fmt.Sprintf("%s/%s", leaf, apiPortName(plan.profile, port, sub))

				if err := g.addConn(ctx, l, spec.Name, labels, wiringapi.ConnectionSpec{
					Unbundled: &wiringapi.ConnUnbundled{
						Link: wiringapi.ServerToSwitchLink{
							Server: wiringapi.BasePortName{Port: server + "/enp2s1"},
							Switch: wiringapi.BasePortName{Port: switchPort},
						},
					},
				}); err != nil {
					return fmt.Errorf("adding connection for server %d: %w", serverIdx+1, err)
				}
			}
		}
	}

	return nil
}

func (g *Generator) generateVPCs(ctx context.Context, l *apiutil.Loader, d *domainGen) error {
	spec, labels := d.spec, d.labels
	if spec.VPCs == 0 {
		return nil
	}

	for idx := range spec.VPCs {
		gateway := d.subnets[idx].Addr().Next()

		if err := l.Add(ctx, &vpcapi.VPC{
			TypeMeta:   kmetav1.TypeMeta{Kind: vpcapi.KindVPC, APIVersion: vpcapi.GroupVersion.String()},
			ObjectMeta: objMeta(d.vpcName(idx), labels),
			Spec: vpcapi.VPCSpec{
				// DS5000 has Features.L2VNI false, so VPCs must be L3VNI. This
				// is set explicitly rather than derived so a mixed-profile
				// fabric fails validation loudly instead of silently.
				Mode:          vpcapi.VPCModeL3VNI,
				Topology:      vpcapi.VPCTopology{Fabric: spec.Name, Domains: []string{d.name}},
				IPv4Namespace: spec.Name,
				VLANNamespace: spec.Name,
				Subnets: map[string]*vpcapi.VPCSubnet{
					"default": {
						Subnet:  d.subnets[idx].String(),
						Gateway: gateway.String(),
						VLAN:    uint16(VLANFrom + d.firstVPC + idx), //nolint:gosec // bounded by MaxVPCsPerFabric
					},
				},
			},
		}); err != nil {
			return fmt.Errorf("adding vpc %d: %w", d.firstVPC+idx+1, err)
		}
	}

	if err := g.generateAttachments(ctx, l, d); err != nil {
		return err
	}

	return g.generatePeerings(ctx, l, d)
}

// generateAttachments attaches each unbundled connection to Attach distinct
// VPCs, spreading servers evenly across the domain's VPCs.
func (g *Generator) generateAttachments(ctx context.Context, l *apiutil.Loader, d *domainGen) error {
	spec, plan, labels := d.spec, d.plan, d.labels
	if spec.Attach == 0 {
		return nil
	}

	perLeaf := spec.ServersPerLeaf()

	for leafIdx := range spec.Leaves {
		leaf := d.leafName(leafIdx)

		for portIdx, port := range plan.serverPorts {
			for sub := range plan.subports {
				serverIdx := leafIdx*perLeaf + uint(portIdx)*plan.subports + sub //nolint:gosec // portIdx is bounded by ServerPorts
				server := d.serverName(serverIdx)

				connSpec := wiringapi.ConnectionSpec{
					Unbundled: &wiringapi.ConnUnbundled{
						Link: wiringapi.ServerToSwitchLink{
							Server: wiringapi.BasePortName{Port: server + "/enp2s1"},
							Switch: wiringapi.BasePortName{Port: fmt.Sprintf("%s/%s", leaf, apiPortName(plan.profile, port, sub))},
						},
					},
				}
				connName := connSpec.GenerateName()

				for j := range spec.Attach {
					vpcIdx := (serverIdx + j) % spec.VPCs
					vpc := d.vpcName(vpcIdx)

					if err := l.Add(ctx, &vpcapi.VPCAttachment{
						TypeMeta:   kmetav1.TypeMeta{Kind: vpcapi.KindVPCAttachment, APIVersion: vpcapi.GroupVersion.String()},
						ObjectMeta: objMeta(fmt.Sprintf("%s--%s", connName, vpc), labels),
						Spec: vpcapi.VPCAttachmentSpec{
							Topology:   vpcapi.VPCAttachmentTopology{Fabric: spec.Name},
							Connection: connName,
							Subnet:     vpc + "/default",
						},
					}); err != nil {
						return fmt.Errorf("adding attachment for server %d vpc %s: %w", serverIdx+1, vpc, err)
					}
				}
			}
		}
	}

	return nil
}

// generatePeerings creates Peerings distinct pairs of the domain's VPCs, in a
// stable order.
func (g *Generator) generatePeerings(ctx context.Context, l *apiutil.Loader, d *domainGen) error {
	spec, labels := d.spec, d.labels
	if spec.Peerings == 0 {
		return nil
	}

	created := uint(0)
	for a := uint(0); a < spec.VPCs && created < spec.Peerings; a++ {
		for b := a + 1; b < spec.VPCs && created < spec.Peerings; b++ {
			vpcA, vpcB := d.vpcName(a), d.vpcName(b)

			if err := l.Add(ctx, &vpcapi.VPCPeering{
				TypeMeta:   kmetav1.TypeMeta{Kind: vpcapi.KindVPCPeering, APIVersion: vpcapi.GroupVersion.String()},
				ObjectMeta: objMeta(fmt.Sprintf("%s--%s", vpcA, vpcB), labels),
				Spec: vpcapi.VPCPeeringSpec{
					Topology: vpcapi.VPCPeeringTopology{Fabric: spec.Name},
					Permit: []map[string]vpcapi.VPCPeer{{
						vpcA: {},
						vpcB: {},
					}},
				},
			}); err != nil {
				return fmt.Errorf("adding peering %s--%s: %w", vpcA, vpcB, err)
			}

			created++
		}
	}

	return nil
}

// vpcSubnets carves one subnet per VPC of every domain out of IPv4Subnet, in
// fabric-wide VPC order, each large enough for its share of a domain's
// attachments. The domains share the fabric's IPv4Namespace, so their subnets
// must not overlap.
func vpcSubnets(spec FabricSpec) ([]netip.Prefix, error) {
	base, err := netip.ParsePrefix(IPv4Subnet)
	if err != nil {
		return nil, fmt.Errorf("parsing bench IPv4 subnet: %w", err)
	}

	total := spec.VPCs * spec.Domains

	perVPC := (spec.Attachments() + spec.VPCs - 1) / spec.VPCs
	// Network address, broadcast and gateway are not usable by a server.
	need := max(perVPC+3, 4)

	hostBits := bits.Len64(uint64(need - 1))
	// VPCSubnet.Validate rejects anything longer than /30.
	prefixBits := min(32-hostBits, 30)
	if prefixBits < base.Bits() {
		return nil, fmt.Errorf("%d vpcs of %d hosts do not fit in %s", total, perVPC, IPv4Subnet) //nolint:err113
	}

	step := uint64(1) << uint(hostBits) //nolint:gosec // hostBits <= 32 by construction

	out := make([]netip.Prefix, 0, total)
	for idx := range total {
		addr, err := addrAdd(base.Addr(), uint64(idx)*step)
		if err != nil {
			return nil, fmt.Errorf("allocating subnet for vpc %d: %w", idx+1, err)
		}

		prefix := netip.PrefixFrom(addr, prefixBits)
		if !base.Contains(addr) {
			return nil, fmt.Errorf("%d vpcs of %d hosts do not fit in %s", total, perVPC, IPv4Subnet) //nolint:err113
		}

		out = append(out, prefix)
	}

	return out, nil
}

// addConn adds a Connection to a fabric under its generated name, which
// depends only on the endpoints.
func (g *Generator) addConn(ctx context.Context, l *apiutil.Loader, fabric string, labels map[string]string, spec wiringapi.ConnectionSpec) error {
	spec.Topology = wiringapi.ConnectionTopology{Fabric: fabric}

	conn := &wiringapi.Connection{
		TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindConnection, APIVersion: wiringapi.GroupVersion.String()},
		ObjectMeta: objMeta(spec.GenerateName(), labels),
		Spec:       spec,
	}

	if err := l.Add(ctx, conn); err != nil {
		return fmt.Errorf("adding connection %s: %w", conn.Name, err)
	}

	return nil
}

// objMeta builds ObjectMeta for a bench object. Everything lives in the default
// namespace, which is where the Fabric controller and the switch profiles are.
func objMeta(name string, labels map[string]string) kmetav1.ObjectMeta {
	return kmetav1.ObjectMeta{
		Name:      name,
		Namespace: kmetav1.NamespaceDefault,
		Labels:    copyLabels(labels),
	}
}

// copyLabels returns a copy so that objects never share a label map.
func copyLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)

	return out
}

// Objects counts what Generate will emit, for the pre-run summary.
func (g *Generator) Objects() map[string]uint {
	out := map[string]uint{}

	for _, spec := range g.specs {
		out["Fabric"]++
		out["VLANNamespace"]++
		out["IPv4Namespace"]++
		out["SwitchGroup"]++
		out["Switch"] += spec.Switches() * spec.Domains
		out["Connection"] += (spec.FabricConns() + spec.Servers()) * spec.Domains
		out["Server"] += spec.Servers() * spec.Domains
		out["VPC"] += spec.VPCs * spec.Domains
		out["VPCAttachment"] += spec.Attachments() * spec.Domains
		out["VPCPeering"] += spec.Peerings * spec.Domains
	}

	return out
}
