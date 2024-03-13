// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wiring

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	agentapi "go.githedgehog.com/fabric/api/agent/v1alpha2"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func createExternal(e agentapi.VirtualEdgeConfig, data *wiring.Data) error {
	external := &vpcapi.External{
		TypeMeta: metav1.TypeMeta{
			Kind:       "External",
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "virtual-edge",
		},
		Spec: vpcapi.ExternalSpec{
			IPv4Namespace:     "default",
			InboundCommunity:  e.CommunityOut,
			OutboundCommunity: e.CommunityIn,
		},
	}
	return errors.Wrapf(data.Add(external), "error adding external object")
}

func createExternalAttachment(e agentapi.VirtualEdgeConfig, data *wiring.Data, conn string) error {
	vlan, err := strconv.ParseUint(e.IfVlan, 10, 16)
	if err != nil {
		return errors.Wrapf(err, "error parsing VLAN %s", e.IfVlan)
	}

	virtualEdgeIPBits := strings.Split(e.IfIP, "/")
	virtualEdgeIP := virtualEdgeIPBits[0]

	attachment := &vpcapi.ExternalAttachment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ExternalAttachment",
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "virtual-edge-attachment",
		},
		Spec: vpcapi.ExternalAttachmentSpec{
			External:   "virtual-edge",
			Connection: conn,
			Switch: vpcapi.ExternalAttachmentSwitch{
				VLAN: uint16(vlan),
				IP:   fmt.Sprintf("%s/24", e.NeighborIP),
			},
			Neighbor: vpcapi.ExternalAttachmentNeighbor{
				ASN: VIRTUAL_EDGE_ASN,
				IP:  virtualEdgeIP,
			},
		},
	}

	return errors.Wrapf(data.Add(attachment), "error adding external attachment object")
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
	VIRTUAL_EDGE_IP_NET  = 40 // single /24 is more than enough
	VIRTUAL_EDGE_CFG     = "virtual-edge.hhfab.fabric.githedgehog.com/external-cfg"
	VIRTUAL_EDGE_ASN     = 64100
)

func HydratePath(wiringPath string) error {
	if wiringPath == "" {
		return errors.Errorf("wiring path is not specified")
	}

	data, err := wiring.New()
	if err != nil {
		return errors.Wrapf(err, "error creating wiring data")
	}
	err = wiring.LoadDataFrom(wiringPath, data)
	if err != nil {
		return errors.Wrapf(err, "error loading wiring data from %s", wiringPath)
	}

	// TODO config
	if err := Hydrate(data, &HydrateConfig{
		Subnet:       "172.30.0.0/16",
		SpineASN:     65100,
		LeafASNStart: 65101,
	}); err != nil {
		return errors.Wrapf(err, "error hydrating wiring data")
	}

	return errors.Wrapf(data.Write(os.Stdout), "error writing wiring data")
}

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
	var externalSwitches []string
	var externalConnections []string
	for _, conn := range data.Connection.All() {
		if conn.Spec.MCLAGDomain != nil {

			sws, _, _, _, err := conn.Spec.Endpoints()
			if err != nil {
				return errors.Wrapf(err, "error getting endpoints for MCLAG domain connection %s", conn.Name)
			}
			if len(sws) != 2 {
				return errors.Errorf("MCLAG domain connection %s has %d endpoints, expected 2", conn.Name, len(sws))
			}

			mclagPeer[sws[0]] = sws[1]
			mclagPeer[sws[1]] = sws[0]
		}
		if conn.Spec.External != nil {

			sws, _, _, _, err := conn.Spec.Endpoints()
			if err != nil {
				return errors.Wrapf(err, "error getting endpoints for external connection %s", conn.Name)
			}
			if len(sws) != 1 {
				return errors.Errorf("external connection %s has %d endpoints, expected 1", conn.Name, len(sws))
			}
			externalSwitches = append(externalSwitches, sws[0])
			externalConnections = append(externalConnections, conn.Name)
		}

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
		if sw.Spec.Role.IsVirtualEdge() {
			sw.Spec.ASN = VIRTUAL_EDGE_ASN
			sw.Spec.IP = fmt.Sprintf("%s.%d.%d/32", cfg.Subnet, SWITCH_IP_NET, leaf+LEAF_OFFSET)
			sw.Spec.ProtocolIP = fmt.Sprintf("%s.%d.%d/32", cfg.Subnet, PROTOCOL_IP_NET, leaf+LEAF_OFFSET)
			if len(externalSwitches) != 1 {
				return errors.Errorf("expected exactly one external switch for virtual edge, got %d", len(externalSwitches))
			}

			if borderSw := data.Switch.Get(externalSwitches[0]); borderSw != nil {
				externalConfig := agentapi.VirtualEdgeConfig{
					ASN:          fmt.Sprintf("%d", borderSw.Spec.ASN),
					VRF:          "default",
					CommunityIn:  fmt.Sprintf("%d:%d", VIRTUAL_EDGE_ASN, borderSw.Spec.ASN),
					CommunityOut: fmt.Sprintf("%d:%d", borderSw.Spec.ASN, VIRTUAL_EDGE_ASN),
					NeighborIP:   fmt.Sprintf("%s.%d.%d", cfg.Subnet, VIRTUAL_EDGE_IP_NET, 1),
					IfName:       "Ethernet1",
					IfVlan:       "200",
					IfIP:         fmt.Sprintf("%s.%d.%d/24", cfg.Subnet, VIRTUAL_EDGE_IP_NET, leaf+LEAF_OFFSET),
				}

				encodedConfig := map[string]agentapi.VirtualEdgeConfig{}
				encodedConfig[borderSw.Name] = externalConfig
				encoded, err := json.Marshal(encodedConfig)
				if err != nil {
					return errors.Wrapf(err, "error encoding external config")
				}
				sw.Annotations = make(map[string]string)
				sw.Annotations[VIRTUAL_EDGE_CFG] = string(encoded)

				err = createExternal(externalConfig, data)
				if err != nil {
					return errors.Wrapf(err, "error creating external object")
				}

				err = createExternalAttachment(externalConfig, data, externalConnections[0])
				if err != nil {
					return errors.Wrapf(err, "error creating external attachment object")
				}
			}
		}

		if err := data.Update(sw); err != nil {
			return errors.Wrapf(err, "error updating switch %s", sw.Name)
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

		if err := data.Update(conn); err != nil {
			return errors.Wrapf(err, "error updating connection %s", conn.Name)
		}
	}

	return nil
}
