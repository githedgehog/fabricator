// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package inspect

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
	"go.githedgehog.com/fabric/api/agent/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	coreapi "k8s.io/api/core/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

type BGPIn struct {
	Switches      []string
	Strict        bool
	GatewayStrict bool // TODO remove after gateway is implemented
}

type BGPOut struct {
	Neighbors map[string]map[string]map[string]apiutil.BGPNeighborStatus `json:"neighbors"`
	Errs      []error                                                    `json:"errors"`
}

func (out *BGPOut) MarshalText(_ BGPIn, now time.Time) (string, error) {
	// TODO pass to a marshal func?
	noColor := !isatty.IsTerminal(os.Stdout.Fd())

	red := color.New(color.FgRed).SprintFunc()
	if noColor {
		red = fmt.Sprint
	}

	str := &strings.Builder{}

	for _, swName := range slices.Sorted(maps.Keys(out.Neighbors)) {
		str.WriteString("Switch: " + swName + "\n")

		data := [][]string{}

		for vrf, neighs := range out.Neighbors[swName] {
			for name, n := range neighs {
				t := string(n.Type)
				if !n.Expected {
					if t != "" {
						t += " (unexpected)"
					} else {
						t = "unexpected"
					}

					t = red(t)
				}

				s := string(n.SessionState)
				if s != string(v1beta1.BGPNeighborSessionStateEstablished) {
					s = red(s)
				}

				data = append(data, []string{
					t,
					n.Port,
					vrf,
					name,
					n.RemoteName,
					n.ConnectionName,
					s,
					HumanizeTime(now, n.LastEstablished.Time),
				})
			}
		}

		str.WriteString(RenderTable(
			[]string{"Type", "Port", "VRF", "Neighbor", "RemoteName", "Connection", "State", "LastEstablished"},
			data,
		))
	}

	return str.String(), nil
}

func (out *BGPOut) Errors() []error {
	return out.Errs
}

var (
	_ Func[BGPIn, *BGPOut] = BGP
	_ WithErrors           = (*BGPOut)(nil)
)

func BGP(ctx context.Context, kube kclient.Reader, in BGPIn) (*BGPOut, error) {
	out := &BGPOut{
		Neighbors: map[string]map[string]map[string]apiutil.BGPNeighborStatus{},
	}

	fabCfgCM := &coreapi.ConfigMap{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: "fabric-ctrl-config", Namespace: "fab"}, fabCfgCM); err != nil {
		return nil, fmt.Errorf("getting fabric-ctrl-config: %w", err)
	}

	fabCfg := &meta.FabricConfig{}
	if err := kyaml.UnmarshalStrict([]byte(fabCfgCM.Data["config.yaml"]), fabCfg); err != nil {
		return nil, fmt.Errorf("unmarshalling fabric config: %w", err)
	}

	if _, err := fabCfg.Init(); err != nil {
		return nil, fmt.Errorf("initializing fabric config: %w", err)
	}

	sws := &wiringapi.SwitchList{}
	if err := kube.List(ctx, sws); err != nil {
		return nil, fmt.Errorf("listing switches: %w", err)
	}

	for _, sw := range sws.Items {
		if len(in.Switches) > 0 && !slices.Contains(in.Switches, sw.Name) {
			continue
		}

		neighs, err := apiutil.GetBGPNeighbors(ctx, kube, fabCfg, &sw)
		if err != nil {
			return nil, fmt.Errorf("getting BGP neighbors for switch %s: %w", sw.Name, err)
		}

		if in.Strict {
			for vrf, vrfNeighbors := range neighs {
				for name, neighbor := range vrfNeighbors {
					if !neighbor.Expected {
						out.Errs = append(out.Errs, fmt.Errorf("switch %s: vrf %s: unexpected neighbor %q", sw.Name, vrf, name)) //nolint:goerr113
					}

					if !in.GatewayStrict && neighbor.Type == apiutil.BGPNeighborTypeGateway {
						continue
					}

					if neighbor.SessionState != v1beta1.BGPNeighborSessionStateEstablished {
						out.Errs = append(out.Errs, fmt.Errorf("switch %s: vrf %s: neighbor %q is not established", sw.Name, vrf, name)) //nolint:goerr113
					}
				}
			}
		}

		out.Neighbors[sw.Name] = neighs
	}

	for _, sw := range in.Switches {
		if _, ok := out.Neighbors[sw]; !ok {
			return nil, fmt.Errorf("switch %s not found", sw) //nolint:goerr113
		}
	}

	return out, nil
}
