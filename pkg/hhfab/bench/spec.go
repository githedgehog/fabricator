// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// Package bench generates and drives synthetic Fabric topologies against a
// running VLAB control node in order to find the scalability limits of the
// Fabric/Fabricator control plane.
//
// The unit of scale is a "fabric": an isolated spine-leaf fabric that shares
// nothing with the others - its own Fabric object and ASNs, switches,
// VLANNamespace, IPv4Namespace, VPCs, VPCAttachments and VPCPeerings.
package bench

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
)

// MaxFabricNameLen is bounded by the VPC name limit: VPCs are named
// "<fabric>-NNN" and vpcapi rejects any name longer than 11 characters (see
// vpc_types.go Validate). The same 11 character limit applies to the
// per-fabric IPv4Namespace, which is named after the fabric alone and so is
// not the binding constraint.
const MaxFabricNameLen = 7

// MaxVPCsPerFabric follows from the "<fabric>-NNN" VPC naming scheme, and
// covers every domain of the fabric since they share the numbering. In
// practice the usable VLANs in a VLANNamespace (1-2999 plus 4000-4094, the
// rest being reserved for VPCIRBVLANs and TH5WorkaroundVLANs) bind first.
const MaxVPCsPerFabric = 999

// MaxDomains is how many domains fit the per-fabric ASN block: the leaf range
// after ASNLeafOffset has room for this many domains' worth of leaves, and the
// spine and gateway ASNs below it for as many of each.
const MaxDomains = (StrideASNs - ASNLeafOffset) / StrideLeaves

// Defaults for a fabric, matching the shape a DS5000 fills exactly: 32 spines
// of 64 ports each (one per leaf), 64 leaves using 32 ports for uplinks and 32
// broken out 4x200G for servers.
const (
	DefaultDomains        = 1
	DefaultSpines         = 32
	DefaultLeaves         = 64
	DefaultFabricLinks    = 1
	DefaultServerPorts    = 32
	DefaultServerBreakout = "4x200G"
	DefaultVPCs           = 1
	DefaultAttach         = 1
	DefaultPeerings       = 0

	// DefaultSysNameOverride is the percentage of servers whose expected LLDP
	// system name differs from their object name, as a real server named by
	// its FQDN would.
	DefaultSysNameOverride = 50
)

// SysNameSuffix is appended to a server's name to make the LLDP system name it
// is expected to advertise when it overrides it.
const SysNameSuffix = "-b"

// DefaultProfile is the switch profile used for both spines and leaves unless
// overridden.
var DefaultProfile = switchprofile.CelesticaDS5000.Name

// fabricNameRe is a lowercase RFC 1123 label, which every object name derived
// from the fabric name has to satisfy.
var fabricNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ServerBreakouts are the breakout modes the bench will configure on
// server-facing leaf ports. Each is validated against the switch profile at
// generation time; this list only bounds what the flag accepts.
var ServerBreakouts = []string{"1x800G", "2x400G", "4x200G"}

// FabricSpecKeys lists every key accepted by ParseFabricSpec, for help text
// and error messages.
var FabricSpecKeys = []string{
	"name", "domains", "spines", "leaves", "fabric-links", "fabric-unnum",
	"server-ports", "server-breakout", "sysname-override", "vpcs", "attach", "peerings", "profile",
}

// FabricSpec describes one synthetic fabric. Every field is per-fabric so that
// heterogeneous fabrics - closer to a real mixed deployment - can be described
// without changing the generator, allocator or preflight.
//
// A fabric has Domains spine layers, and every other count describes one
// domain: each is a full copy of the shape, and nothing - connections, VPCs,
// attachments, peerings - crosses from one domain to another. The shape
// helpers below are per domain too.
type FabricSpec struct {
	Name string

	Domains uint // spine layers, each a full copy of the shape below

	Spines uint // spine switches
	Leaves uint // leaf switches

	FabricLinks uint // links between each spine-leaf pair
	FabricUnnum bool // BGP-unnumbered fabric links instead of /31 pairs

	ServerPorts    uint   // leaf ports facing servers
	ServerBreakout string // breakout mode for those ports

	SysNameOverride uint // percent of servers expected to advertise an LLDP system name other than their own

	VPCs     uint // VPCs
	Attach   uint // VPCAttachments per unbundled connection, each to a distinct VPC
	Peerings uint // VPCPeerings

	Profile string // switch profile for spines and leaves
}

// DefaultFabricSpec returns a spec with every field at its default. The caller
// is expected to set Name.
func DefaultFabricSpec() FabricSpec {
	return FabricSpec{
		Domains:         DefaultDomains,
		Spines:          DefaultSpines,
		Leaves:          DefaultLeaves,
		FabricLinks:     DefaultFabricLinks,
		ServerPorts:     DefaultServerPorts,
		ServerBreakout:  DefaultServerBreakout,
		SysNameOverride: DefaultSysNameOverride,
		VPCs:            DefaultVPCs,
		Attach:          DefaultAttach,
		Peerings:        DefaultPeerings,
		Profile:         DefaultProfile,
	}
}

// ServerSubportsPerPort is how many server-facing subports one physical port
// yields under the configured breakout mode.
func (f FabricSpec) ServerSubportsPerPort() uint {
	switch f.ServerBreakout {
	case "1x800G":
		return 1
	case "2x400G":
		return 2
	case "4x200G":
		return 4
	default:
		return 0
	}
}

// Switches is the total number of switches in this fabric.
func (f FabricSpec) Switches() uint {
	return f.Spines + f.Leaves
}

// FabricConns is the number of fabric Connections: one per spine-leaf pair,
// each carrying FabricLinks links.
func (f FabricSpec) FabricConns() uint {
	return f.Spines * f.Leaves
}

// FabricLinksTotal is the number of individual fabric links, which is what
// consumes /31 pairs out of the fabric subnet.
func (f FabricSpec) FabricLinksTotal() uint {
	return f.Spines * f.Leaves * f.FabricLinks
}

// ServersPerLeaf is how many servers attach to one leaf.
func (f FabricSpec) ServersPerLeaf() uint {
	return f.ServerPorts * f.ServerSubportsPerPort()
}

// Servers is the total number of Servers, which equals the number of unbundled
// Connections since each server gets exactly one.
func (f FabricSpec) Servers() uint {
	return f.Leaves * f.ServersPerLeaf()
}

// Attachments is the total number of VPCAttachments.
func (f FabricSpec) Attachments() uint {
	return f.Servers() * f.Attach
}

// OverridesSysName reports whether the server at idx within its domain is
// expected to advertise an LLDP system name other than its own. The overrides
// are spread evenly - every other server at 50% - rather than bunched, and
// exactly floor(n * SysNameOverride / 100) of n servers get one.
func (f FabricSpec) OverridesSysName(idx uint) bool {
	return (idx+1)*f.SysNameOverride/100 > idx*f.SysNameOverride/100
}

// DomainName is the name of a domain, by index. A single domain keeps the
// default name, so a one-domain fabric looks exactly like any other Fabric.
func (f FabricSpec) DomainName(domain uint) string {
	if f.Domains == 1 {
		return wiringapi.DefaultFabricDomain
	}

	return fmt.Sprintf("domain-%d", domain+1)
}

// Objects is a rough count of the API objects this fabric creates directly,
// across all its domains. It excludes the Agent and the five RBAC/Secret
// objects the Fabric controller mints per switch.
func (f FabricSpec) Objects() uint {
	perDomain := f.Switches() +
		f.FabricConns() +
		f.Servers()*2 + // Server + unbundled Connection
		f.Attachments() +
		f.VPCs +
		f.Peerings

	return perDomain*f.Domains +
		4 // Fabric, SwitchGroup, VLANNamespace, IPv4Namespace
}

// Validate checks the spec in isolation. Limits that depend on the switch
// profile (port counts, pipeline capacity) are checked by the generator, and
// limits that depend on the cluster (ASN and subnet capacity) by the allocator
// preflight.
func (f FabricSpec) Validate() error {
	if f.Name == "" {
		return fmt.Errorf("name is required") //nolint:err113
	}
	if len(f.Name) > MaxFabricNameLen {
		return fmt.Errorf("name %q is %d characters, maximum is %d (VPCs are named <fabric>-NNN and are capped at 11)", //nolint:err113
			f.Name, len(f.Name), MaxFabricNameLen)
	}
	if !fabricNameRe.MatchString(f.Name) {
		return fmt.Errorf("name %q must be a lowercase RFC 1123 label", f.Name) //nolint:err113
	}
	// Each bench fabric is a Fabric object of the same name, and the default
	// one belongs to the cluster: the bench must never redefine or delete it.
	if f.Name == wiringapi.DefaultFabric {
		return fmt.Errorf("name %q is reserved for the cluster's own Fabric", f.Name) //nolint:err113
	}

	if f.Domains < 1 || f.Domains > MaxDomains {
		return fmt.Errorf("domains must be between 1 and %d", MaxDomains) //nolint:err113
	}

	if f.Spines < 1 {
		return fmt.Errorf("spines must be >= 1") //nolint:err113
	}
	if f.Leaves < 1 {
		return fmt.Errorf("leaves must be >= 1") //nolint:err113
	}
	if f.FabricLinks < 1 {
		return fmt.Errorf("fabric-links must be >= 1") //nolint:err113
	}

	if !slices.Contains(ServerBreakouts, f.ServerBreakout) {
		return fmt.Errorf("server-breakout %q must be one of %s", f.ServerBreakout, strings.Join(ServerBreakouts, ", ")) //nolint:err113
	}

	if f.SysNameOverride > 100 {
		return fmt.Errorf("sysname-override is a percentage of servers, got %d", f.SysNameOverride) //nolint:err113
	}

	if f.VPCs*f.Domains > MaxVPCsPerFabric {
		return fmt.Errorf("%d vpcs in each of %d domains requested, maximum is %d in total with the <fabric>-NNN naming scheme", //nolint:err113
			f.VPCs, f.Domains, MaxVPCsPerFabric)
	}

	if f.Attach > f.VPCs {
		return fmt.Errorf("attach=%d but vpcs=%d, each attachment goes to a distinct VPC", f.Attach, f.VPCs) //nolint:err113
	}

	if f.Peerings > 0 {
		if f.VPCs < 2 {
			return fmt.Errorf("peerings=%d requires at least 2 vpcs, have %d", f.Peerings, f.VPCs) //nolint:err113
		}

		maxPeerings := f.VPCs * (f.VPCs - 1) / 2
		if f.Peerings > maxPeerings {
			return fmt.Errorf("peerings=%d but %d vpcs allow at most %d distinct pairs", f.Peerings, f.VPCs, maxPeerings) //nolint:err113
		}
	}

	if f.Profile == "" {
		return fmt.Errorf("profile must not be empty") //nolint:err113
	}

	return nil
}

// ParseFabricSpec parses one --fabric value, e.g.
// "name=dc1,spines=8,leaves=16,server-breakout=2x400G". Unset keys take their
// default.
func ParseFabricSpec(value string) (FabricSpec, error) {
	spec := DefaultFabricSpec()

	for part := range strings.SplitSeq(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		key, val, found := strings.Cut(part, "=")
		if !found {
			return spec, fmt.Errorf("%q should be key=value", part) //nolint:err113
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)

		var err error
		switch key {
		case "name":
			spec.Name = val
		case "domains":
			spec.Domains, err = parseUint(key, val)
		case "spines":
			spec.Spines, err = parseUint(key, val)
		case "leaves":
			spec.Leaves, err = parseUint(key, val)
		case "fabric-links":
			spec.FabricLinks, err = parseUint(key, val)
		case "fabric-unnum":
			spec.FabricUnnum, err = parseBool(key, val)
		case "server-ports":
			spec.ServerPorts, err = parseUint(key, val)
		case "server-breakout":
			spec.ServerBreakout = val
		case "sysname-override":
			spec.SysNameOverride, err = parseUint(key, val)
		case "vpcs":
			spec.VPCs, err = parseUint(key, val)
		case "attach":
			spec.Attach, err = parseUint(key, val)
		case "peerings":
			spec.Peerings, err = parseUint(key, val)
		case "profile":
			spec.Profile = val
		default:
			return spec, fmt.Errorf("unknown key %q, valid keys are %s", key, strings.Join(FabricSpecKeys, ", ")) //nolint:err113
		}
		if err != nil {
			return spec, err
		}
	}

	return spec, spec.Validate()
}

// ParseFabricSpecs parses every --fabric value and rejects duplicate names.
// The slice order is significant: it determines each fabric's address slot.
func ParseFabricSpecs(values []string) ([]FabricSpec, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one --fabric is required") //nolint:err113
	}

	specs := make([]FabricSpec, 0, len(values))
	seen := map[string]int{}

	for idx, value := range values {
		spec, err := ParseFabricSpec(value)
		if err != nil {
			return nil, fmt.Errorf("--fabric %d (%q): %w", idx+1, value, err)
		}

		if prev, exist := seen[spec.Name]; exist {
			return nil, fmt.Errorf("duplicate fabric name %q, used by --fabric %d and %d", spec.Name, prev+1, idx+1) //nolint:err113
		}
		seen[spec.Name] = idx

		specs = append(specs, spec)
	}

	return specs, nil
}

func parseUint(key, val string) (uint, error) {
	parsed, err := strconv.ParseUint(val, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a non-negative integer", key, val) //nolint:err113
	}

	return uint(parsed), nil
}

func parseBool(key, val string) (bool, error) {
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean", key, val) //nolint:err113
	}

	return parsed, nil
}
