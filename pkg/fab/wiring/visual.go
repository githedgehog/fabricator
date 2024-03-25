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
	"fmt"
	"sort"

	"github.com/pkg/errors"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/util/pointer"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/wiring/visual"
)

func Visualize(wiringPath string) (string, error) {
	if wiringPath == "" {
		return "", errors.Errorf("wiring path is not specified")
	}

	data, err := wiring.New()
	if err != nil {
		return "", errors.Wrapf(err, "error creating wiring data")
	}
	err = wiring.LoadDataFrom(wiringPath, data)
	if err != nil {
		return "", errors.Wrapf(err, "error loading wiring data from %s", wiringPath)
	}

	vis := visual.New()

	endpoints := map[string][]visual.Endpoint{}

	for _, conn := range data.Connection.All() {
		if conn.Spec.Management != nil {
			link := conn.Spec.Management.Link

			endpoints[link.Server.DeviceName()] = append(endpoints[link.Server.DeviceName()], visual.Endpoint{
				ID:   endpointID(&link.Server),
				Name: link.Server.LocalPortName(),
				Properties: map[string]string{
					"ip": link.Server.IP,
				},
			})
			endpoints[link.Switch.DeviceName()] = append(endpoints[link.Switch.DeviceName()], visual.Endpoint{
				ID:   endpointID(&link.Switch),
				Name: link.Switch.LocalPortName(),
				Properties: map[string]string{
					"ip": link.Switch.IP,
				},
			})

			vis.Links = append(vis.Links, visual.Link{
				From:  endpointID(&link.Server),
				To:    endpointID(&link.Switch),
				Color: "red",
			})
		} else if conn.Spec.Fabric != nil {
			for _, link := range conn.Spec.Fabric.Links {
				endpoints[link.Spine.DeviceName()] = append(endpoints[link.Spine.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Spine)),
					Name: link.Spine.LocalPortName(),
					Properties: map[string]string{
						"ip": link.Spine.IP,
					},
				})
				endpoints[link.Leaf.DeviceName()] = append(endpoints[link.Leaf.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Leaf)),
					Name: link.Leaf.LocalPortName(),
					Properties: map[string]string{
						"ip": link.Leaf.IP,
					},
				})

				vis.Links = append(vis.Links, visual.Link{
					From:  endpointID(pointer.To(link.Spine)),
					To:    endpointID(pointer.To(link.Leaf)),
					Color: "orange",
				})
			}
		} else if conn.Spec.MCLAGDomain != nil {
			for _, link := range append(conn.Spec.MCLAGDomain.PeerLinks, conn.Spec.MCLAGDomain.SessionLinks...) {
				endpoints[link.Switch1.DeviceName()] = append(endpoints[link.Switch1.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Switch1)),
					Name: link.Switch1.LocalPortName(),
				})
				endpoints[link.Switch2.DeviceName()] = append(endpoints[link.Switch2.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Switch2)),
					Name: link.Switch2.LocalPortName(),
				})

				vis.Links = append(vis.Links, visual.Link{
					From:  endpointID(pointer.To(link.Switch1)),
					To:    endpointID(pointer.To(link.Switch2)),
					Color: "green",
				})
			}
		} else if conn.Spec.VPCLoopback != nil {
			for _, link := range conn.Spec.VPCLoopback.Links {
				endpoints[link.Switch1.DeviceName()] = append(endpoints[link.Switch1.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Switch1)),
					Name: link.Switch1.LocalPortName(),
				})
				endpoints[link.Switch2.DeviceName()] = append(endpoints[link.Switch2.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Switch2)),
					Name: link.Switch2.LocalPortName(),
				})

				vis.Links = append(vis.Links, visual.Link{
					From:  endpointID(pointer.To(link.Switch1)),
					To:    endpointID(pointer.To(link.Switch2)),
					Color: "magenta",
				})
			}
		} else if conn.Spec.Unbundled != nil {
			link := conn.Spec.Unbundled.Link

			endpoints[link.Server.DeviceName()] = append(endpoints[link.Server.DeviceName()], visual.Endpoint{
				ID:   endpointID(&link.Server),
				Name: link.Server.LocalPortName(),
			})
			endpoints[link.Switch.DeviceName()] = append(endpoints[link.Switch.DeviceName()], visual.Endpoint{
				ID:   endpointID(&link.Switch),
				Name: link.Switch.LocalPortName(),
			})

			vis.Links = append(vis.Links, visual.Link{
				From:  endpointID(&link.Server),
				To:    endpointID(&link.Switch),
				Color: "black",
			})
		} else if conn.Spec.Bundled != nil {
			for _, link := range conn.Spec.Bundled.Links {
				endpoints[link.Server.DeviceName()] = append(endpoints[link.Server.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Server)),
					Name: link.Server.LocalPortName(),
				})
				endpoints[link.Switch.DeviceName()] = append(endpoints[link.Switch.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Switch)),
					Name: link.Switch.LocalPortName(),
				})

				vis.Links = append(vis.Links, visual.Link{
					From:  endpointID(pointer.To(link.Server)),
					To:    endpointID(pointer.To(link.Switch)),
					Color: "blue",
				})
			}
		} else if conn.Spec.MCLAG != nil {
			for _, link := range conn.Spec.MCLAG.Links {
				endpoints[link.Server.DeviceName()] = append(endpoints[link.Server.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Server)),
					Name: link.Server.LocalPortName(),
				})
				endpoints[link.Switch.DeviceName()] = append(endpoints[link.Switch.DeviceName()], visual.Endpoint{
					ID:   endpointID(pointer.To(link.Switch)),
					Name: link.Switch.LocalPortName(),
				})

				vis.Links = append(vis.Links, visual.Link{
					From:  endpointID(pointer.To(link.Server)),
					To:    endpointID(pointer.To(link.Switch)),
					Color: "purple",
				})
			}
		}
	}

	for _, role := range wiringapi.SwitchRoles {
		for _, sw := range data.Switch.All() {
			if sw.Spec.Role != role {
				continue
			}

			devEndpoints := endpoints[sw.Name]
			sort.Slice(devEndpoints, func(i, j int) bool {
				return devEndpoints[i].ID < devEndpoints[j].ID
			})

			vis.Devices = append(vis.Devices, visual.Device{
				ID:        sw.Name,
				Name:      sw.Name,
				Endpoints: devEndpoints,
				Properties: map[string]string{
					"role":      string(sw.Spec.Role),
					"asn":       fmt.Sprintf("%d", sw.Spec.ASN),
					"switch-ip": sw.Spec.IP,
				},
			})
		}
	}

	for _, role := range []wiringapi.ServerType{wiringapi.ServerTypeControl, wiringapi.ServerTypeDefault} {
		for _, srv := range data.Server.All() {
			if srv.Spec.Type != role {
				continue
			}

			devEndpoints := endpoints[srv.Name]
			sort.Slice(devEndpoints, func(i, j int) bool {
				return devEndpoints[i].ID < devEndpoints[j].ID
			})

			vis.Devices = append(vis.Devices, visual.Device{
				ID:        srv.Name,
				Name:      srv.Name,
				Endpoints: devEndpoints,
				Properties: map[string]string{
					"type": string(srv.Spec.Type),
				},
			})
		}
	}

	res, err := vis.Dot()

	return res, errors.Wrapf(err, "error generating graph")
}

func endpointID(port wiringapi.IPort) string {
	return fmt.Sprintf("%s--%s", port.DeviceName(), port.LocalPortName())
}
