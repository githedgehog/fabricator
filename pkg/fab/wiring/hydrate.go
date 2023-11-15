package wiring

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pkg/errors"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
)

func IsHydrated(data *wiring.Data) error {
	for _, sw := range data.Switch.All() {
		if sw.Spec.Role == "" {
			return errors.Errorf("role not set for switch %s", sw.Name)
		}
		if !slices.Contains(wiringapi.SwitchRoles, sw.Spec.Role) {
			return errors.Errorf("role %s not valid for switch %s", sw.Spec.Role, sw.Name)
		}

		if sw.Spec.ASN == 0 {
			return errors.Errorf("ASN not set for switch %s", sw.Name)
		}
		if sw.Spec.IP == "" {
			return errors.Errorf("IP not set for switch %s", sw.Name)
		}
	}

	for _, conn := range data.Connection.All() {
		if conn.Spec.Management != nil {
			link := conn.Spec.Management.Link

			if link.Server.IP == "" {
				return errors.Errorf("server IP not set for management link %s", conn.Name)
			}
			if link.Switch.IP == "" {
				return errors.Errorf("switch IP not set for management link %s", conn.Name)
			}
		}

		if conn.Spec.Fabric != nil {
			for linkIdx, link := range conn.Spec.Fabric.Links {
				if link.Spine.IP == "" {
					return errors.Errorf("spine IP not set for fabric conn %s/%d", conn.Name, linkIdx)
				}
				if link.Leaf.IP == "" {
					return errors.Errorf("leaf IP not set for fabric conn %s/%d", conn.Name, linkIdx)
				}
			}
		}
	}

	return nil
}

type HydrateConfig struct {
	Subnet       string
	SpineASN     uint32
	LeafASNStart uint32
}

const (
	SPINE_OFFSET = 200
	LEAF_OFFSET  = 100

	// 1 is reserved for the control VIP
	MCLAG_SESSION_IP_NET = 5
	SWITCH_IP_NET        = 10
	PROTOCOL_IP_NET      = 11
	VTEP_IP_NET          = 12
	CONTROL_IP_NET       = 20 // single /24 is more than enough
	FABRIC_IP_NET        = 30 // can take more than one /24, let's book 10
)

func Hydrate(data *wiring.Data, cfg *HydrateConfig) error {
	if !strings.HasSuffix(cfg.Subnet, ".0.0/16") {
		return errors.Errorf("Subnet %s is expected to be x.y.0.0/16", cfg.Subnet)
	}
	cfg.Subnet = strings.TrimSuffix(cfg.Subnet, ".0.0/16")

	// make sure we don't have any leftover data
	for _, sw := range data.Switch.All() {
		sw.Spec.ASN = 0
		sw.Spec.IP = ""
		sw.Spec.VTEPIP = ""
		sw.Spec.ProtocolIP = ""
	}

	mclagPeer := map[string]string{}
	for _, conn := range data.Connection.All() {
		if conn.Spec.MCLAGDomain == nil {
			continue
		}

		sws, _, _, err := conn.Spec.Endpoints()
		if err != nil {
			return errors.Wrapf(err, "error getting endpoints for MCLAG domain connection %s", conn.Name)
		}
		if len(sws) != 2 {
			return errors.Errorf("MCLAG domain connection %s has %d endpoints, expected 2", conn.Name, len(sws))
		}

		mclagPeer[sws[0]] = sws[1]
		mclagPeer[sws[1]] = sws[0]
	}

	spine := 0
	leaf := 0
	for _, sw := range data.Switch.All() {
		if sw.Spec.Role.IsSpine() {
			sw.Spec.ASN = cfg.SpineASN
			sw.Spec.IP = fmt.Sprintf("%s.%d.%d/32", cfg.Subnet, SWITCH_IP_NET, spine+SPINE_OFFSET)
			sw.Spec.ProtocolIP = fmt.Sprintf("%s.%d.%d/32", cfg.Subnet, PROTOCOL_IP_NET, spine+SPINE_OFFSET)

			spine++
		}
		if sw.Spec.Role.IsLeaf() {
			sw.Spec.ASN = cfg.LeafASNStart + uint32(leaf)
			sw.Spec.IP = fmt.Sprintf("%s.%d.%d/32", cfg.Subnet, SWITCH_IP_NET, leaf+LEAF_OFFSET)
			sw.Spec.ProtocolIP = fmt.Sprintf("%s.%d.%d/32", cfg.Subnet, PROTOCOL_IP_NET, leaf+LEAF_OFFSET)
			sw.Spec.VTEPIP = fmt.Sprintf("%s.%d.%d/32", cfg.Subnet, VTEP_IP_NET, leaf+LEAF_OFFSET)

			// MCLAG pair should have the same ASN and VTEP IP
			if peer, ok := mclagPeer[sw.Name]; ok {
				if peerSw := data.Switch.Get(peer); peerSw != nil {
					if peerSw.Spec.ASN != 0 && peerSw.Spec.VTEPIP != "" {
						sw.Spec.ASN = peerSw.Spec.ASN
						sw.Spec.VTEPIP = peerSw.Spec.VTEPIP
					}
				}
			}

			leaf++
		}
	}

	control := 0
	fabric := 0
	fabricNet := 0
	for _, conn := range data.Connection.All() {
		if conn.Spec.Management != nil {
			conn.Spec.Management.Link.Server.IP = fmt.Sprintf("%s.%d.%d/31", cfg.Subnet, CONTROL_IP_NET, control)
			conn.Spec.Management.Link.Switch.IP = fmt.Sprintf("%s.%d.%d/31", cfg.Subnet, CONTROL_IP_NET, control+1)

			control += 2
		}

		if conn.Spec.Fabric != nil {
			for idx := range conn.Spec.Fabric.Links {
				conn.Spec.Fabric.Links[idx].Spine.IP = fmt.Sprintf("%s.%d.%d/31", cfg.Subnet, FABRIC_IP_NET+fabricNet, fabric)
				conn.Spec.Fabric.Links[idx].Leaf.IP = fmt.Sprintf("%s.%d.%d/31", cfg.Subnet, FABRIC_IP_NET+fabricNet, fabric+1)

				fabric += 2
				if fabric > 254 {
					fabricNet++
					if fabricNet > 9 {
						return errors.Errorf("too many fabric connections, ran out of IPs")
					}
				}
			}
		}
	}

	return nil
}