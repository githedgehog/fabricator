// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

// Reasons the disruption suite is skipped. A skip says nothing about the fabric.
var (
	errDisruptionNoPair        = errors.New("no ESLAG redundancy group of two leaves")
	errDisruptionNoESLAGServer = errors.New("no ESLAG server attached to both leaves of the pair")
	errDisruptionNoSingleLeaf  = errors.New("no server attached to a single leaf of the pair")
)

type disruptionServer struct {
	Name string
	Conn wiringapi.Connection
}

// disruptionScenario is the ESLAG pair the disruption suite runs on.
type disruptionScenario struct {
	Group string
	// Leaves are the two leaves of the pair, sorted by name.
	Leaves [2]string
	// SingleLeaf holds, per leaf, the servers attached to that leaf alone, by name.
	SingleLeaf map[string][]disruptionServer
	// ESLAG holds the servers attached to both leaves, by name.
	ESLAG []disruptionServer
}

// pickDisruptionScenario finds the ESLAG pair to run on: two leaves in one redundancy group, an
// ESLAG server on both, and a server on only one of them. extLeaves counts the external
// attachments per leaf; a pair with externals on both leaves is preferred over one without,
// then the first group by name wins. Servers with more than one connection are left out, their
// traffic would not belong to a single leaf.
func pickDisruptionScenario(switches []wiringapi.Switch, conns []wiringapi.Connection, extLeaves map[string]int) (*disruptionScenario, error) {
	groups := map[string][]string{}
	for _, sw := range switches {
		if !sw.Spec.Role.IsLeaf() || sw.Spec.Redundancy.Type != meta.RedundancyTypeESLAG || sw.Spec.Redundancy.Group == "" {
			continue
		}
		groups[sw.Spec.Redundancy.Group] = append(groups[sw.Spec.Redundancy.Group], sw.Name)
	}

	connsPerServer := map[string]int{}
	sorted := slices.Clone(conns)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, c := range sorted {
		_, servers, _, _, err := c.Spec.Endpoints()
		if err != nil {
			continue
		}
		for _, s := range servers {
			connsPerServer[s]++
		}
	}

	var candidates []*disruptionScenario
	sawESLAGServer := false
	for _, group := range sortedKeys(groups) {
		leaves := groups[group]
		if len(leaves) != 2 {
			continue
		}
		sort.Strings(leaves)
		sc := &disruptionScenario{Group: group, Leaves: [2]string{leaves[0], leaves[1]}, SingleLeaf: map[string][]disruptionServer{}}

		for _, c := range sorted {
			sws, servers, _, _, err := c.Spec.Endpoints()
			if err != nil || len(servers) != 1 || connsPerServer[servers[0]] != 1 {
				continue
			}
			srv := disruptionServer{Name: servers[0], Conn: c}
			switch {
			case c.Spec.ESLAG != nil:
				if len(sws) == 2 && slices.Contains(sws, sc.Leaves[0]) && slices.Contains(sws, sc.Leaves[1]) {
					sc.ESLAG = append(sc.ESLAG, srv)
				}
			case c.Spec.Unbundled != nil || c.Spec.Bundled != nil:
				if len(sws) == 1 && slices.Contains(sc.Leaves[:], sws[0]) {
					sc.SingleLeaf[sws[0]] = append(sc.SingleLeaf[sws[0]], srv)
				}
			}
		}
		if len(sc.ESLAG) > 0 {
			sawESLAGServer = true
		}
		if len(sc.ESLAG) == 0 || len(sc.SingleLeaf[sc.Leaves[0]])+len(sc.SingleLeaf[sc.Leaves[1]]) == 0 {
			continue
		}
		candidates = append(candidates, sc)
	}

	if len(candidates) == 0 {
		switch {
		case len(groups) == 0 || !slices.ContainsFunc(sortedKeys(groups), func(g string) bool { return len(groups[g]) == 2 }):
			return nil, errDisruptionNoPair
		case !sawESLAGServer:
			return nil, errDisruptionNoESLAGServer
		default:
			return nil, errDisruptionNoSingleLeaf
		}
	}

	withExt := func(sc *disruptionScenario) bool {
		return extLeaves[sc.Leaves[0]] > 0 && extLeaves[sc.Leaves[1]] > 0
	}
	sort.SliceStable(candidates, func(i, j int) bool { return withExt(candidates[i]) && !withExt(candidates[j]) })

	return candidates[0], nil
}

// describe is the scenario in one line for logs and failure messages.
func (sc *disruptionScenario) describe() string {
	names := func(servers []disruptionServer) []string {
		out := make([]string, 0, len(servers))
		for _, s := range servers {
			out = append(out, s.Name)
		}

		return out
	}

	return fmt.Sprintf("group %s: %s only %v, %s only %v, eslag %v", sc.Group,
		sc.Leaves[0], names(sc.SingleLeaf[sc.Leaves[0]]), sc.Leaves[1], names(sc.SingleLeaf[sc.Leaves[1]]), names(sc.ESLAG))
}
