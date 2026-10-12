// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func dtLeaf(name, group string) wiringapi.Switch {
	sw := wiringapi.Switch{ObjectMeta: kmetav1.ObjectMeta{Name: name}}
	sw.Spec.Role = wiringapi.SwitchRoleServerLeaf
	if group != "" {
		sw.Spec.Redundancy = wiringapi.SwitchRedundancy{Group: group, Type: meta.RedundancyTypeESLAG}
	}

	return sw
}

// dtLink names its ports after the server and the index, so that no two links share a port
func dtLink(server, sw string, idx int) wiringapi.ServerToSwitchLink {
	return wiringapi.ServerToSwitchLink{
		Server: wiringapi.BasePortName{Port: fmt.Sprintf("%s/enp2s%d", server, idx)},
		Switch: wiringapi.BasePortName{Port: fmt.Sprintf("%s/E1/%s-%d", sw, server, idx)},
	}
}

func dtESLAG(server string, leaves ...string) wiringapi.Connection {
	spec := &wiringapi.ConnESLAG{}
	for i, l := range leaves {
		spec.Links = append(spec.Links, dtLink(server, l, i+1))
	}

	return wiringapi.Connection{
		ObjectMeta: kmetav1.ObjectMeta{Name: server + "--eslag"},
		Spec:       wiringapi.ConnectionSpec{ESLAG: spec},
	}
}

func dtUnbundled(server, leaf string) wiringapi.Connection {
	return wiringapi.Connection{
		ObjectMeta: kmetav1.ObjectMeta{Name: server + "--unbundled--" + leaf},
		Spec:       wiringapi.ConnectionSpec{Unbundled: &wiringapi.ConnUnbundled{Link: dtLink(server, leaf, 1)}},
	}
}

func dtBundled(server, leaf string) wiringapi.Connection {
	return wiringapi.Connection{
		ObjectMeta: kmetav1.ObjectMeta{Name: server + "--bundled--" + leaf},
		Spec: wiringapi.ConnectionSpec{Bundled: &wiringapi.ConnBundled{
			Links: []wiringapi.ServerToSwitchLink{dtLink(server, leaf, 1), dtLink(server, leaf, 2)},
		}},
	}
}

func TestPickDisruptionScenarioVLAB(t *testing.T) {
	// the CI VLAB: one pair, two ESLAG servers, two unbundled on the first leaf, one bundled on the second,
	// and an orphan leaf with its own server
	switches := []wiringapi.Switch{dtLeaf("leaf-01", "eslag-1"), dtLeaf("leaf-02", "eslag-1"), dtLeaf("leaf-03", "")}
	conns := []wiringapi.Connection{
		dtESLAG("server-01", "leaf-01", "leaf-02"), dtESLAG("server-02", "leaf-01", "leaf-02"),
		dtUnbundled("server-03", "leaf-01"), dtUnbundled("server-04", "leaf-01"),
		dtBundled("server-05", "leaf-02"),
		dtUnbundled("server-06", "leaf-03"),
	}

	sc, err := pickDisruptionScenario(switches, conns, nil)
	require.NoError(t, err)
	require.Equal(t, "eslag-1", sc.Group)
	require.Equal(t, [2]string{"leaf-01", "leaf-02"}, sc.Leaves)
	require.Len(t, sc.ESLAG, 2)
	require.Equal(t, "server-03", sc.SingleLeaf["leaf-01"][0].Name)
	require.Len(t, sc.SingleLeaf["leaf-01"], 2)
	require.Len(t, sc.SingleLeaf["leaf-02"], 1)
	require.NotContains(t, sc.SingleLeaf, "leaf-03")
}

func TestPickDisruptionScenarioPrefersPairWithExternals(t *testing.T) {
	// the lab-ci environment: eslag-1 sorts first but has no external attachments
	switches := []wiringapi.Switch{
		dtLeaf("ds3000-01", "eslag-1"), dtLeaf("ds3000-02", "eslag-1"),
		dtLeaf("ds3000-03", "eslag-2"), dtLeaf("sse-c4632-01", "eslag-2"),
	}
	conns := []wiringapi.Connection{
		dtESLAG("server-1", "ds3000-01", "ds3000-02"), dtBundled("server-3", "ds3000-01"),
		dtESLAG("server-5", "ds3000-03", "sse-c4632-01"),
		dtUnbundled("server-7", "ds3000-03"), dtUnbundled("server-8", "ds3000-03"), dtUnbundled("server-9", "sse-c4632-01"),
	}

	sc, err := pickDisruptionScenario(switches, conns, map[string]int{"ds3000-03": 3, "sse-c4632-01": 2})
	require.NoError(t, err)
	require.Equal(t, "eslag-2", sc.Group)
	require.Equal(t, [2]string{"ds3000-03", "sse-c4632-01"}, sc.Leaves)

	sc, err = pickDisruptionScenario(switches, conns, nil)
	require.NoError(t, err)
	require.Equal(t, "eslag-1", sc.Group)
}

func TestPickDisruptionScenarioSkipReasons(t *testing.T) {
	_, err := pickDisruptionScenario([]wiringapi.Switch{dtLeaf("leaf-01", "")}, nil, nil)
	require.ErrorIs(t, err, errDisruptionNoPair)

	pair := []wiringapi.Switch{dtLeaf("leaf-01", "eslag-1"), dtLeaf("leaf-02", "eslag-1")}
	_, err = pickDisruptionScenario(pair, []wiringapi.Connection{dtUnbundled("server-01", "leaf-01")}, nil)
	require.ErrorIs(t, err, errDisruptionNoESLAGServer)

	_, err = pickDisruptionScenario(pair, []wiringapi.Connection{dtESLAG("server-01", "leaf-01", "leaf-02")}, nil)
	require.ErrorIs(t, err, errDisruptionNoSingleLeaf)

	// a multihomed server doesn't count as attached to a single leaf
	_, err = pickDisruptionScenario(pair, []wiringapi.Connection{
		dtESLAG("server-01", "leaf-01", "leaf-02"),
		dtUnbundled("server-02", "leaf-01"),
		{ObjectMeta: kmetav1.ObjectMeta{Name: "server-02--unbundled--leaf-02"}, Spec: dtUnbundled("server-02", "leaf-02").Spec},
	}, nil)
	require.ErrorIs(t, err, errDisruptionNoSingleLeaf)
}
