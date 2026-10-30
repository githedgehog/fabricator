// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/pkg/hhfctl/inspect"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
)

func TestGatewayLLDPErrors(t *testing.T) {
	gw := func(actual ...apiutil.LLDPNeighbor) apiutil.LLDPNeighborStatus {
		return apiutil.LLDPNeighborStatus{
			Type:     apiutil.LLDPNeighborTypeGateway,
			Expected: apiutil.LLDPNeighbor{Name: "gateway-1", Port: "port1"},
			Actual:   actual,
		}
	}

	for _, test := range []struct {
		name      string
		neighbors map[string]map[string]apiutil.LLDPNeighborStatus
		errs      []string
	}{
		{
			name: "no neighbors",
		},
		{
			name: "gateway seen with another port name",
			neighbors: map[string]map[string]apiutil.LLDPNeighborStatus{
				"leaf-1": {"E1/3": gw(apiutil.LLDPNeighbor{Name: "gateway-1", Port: "enp5s0"})},
			},
		},
		{
			name: "gateway seen with an ignored suffix",
			neighbors: map[string]map[string]apiutil.LLDPNeighborStatus{
				"leaf-1": {"E1/3": gw(apiutil.LLDPNeighbor{Name: "gateway-1.lan", IgnoredSuffix: ".lan"})},
			},
		},
		{
			name: "gateway missing",
			neighbors: map[string]map[string]apiutil.LLDPNeighborStatus{
				"leaf-1": {"E1/3": gw()},
			},
			errs: []string{`switch leaf-1: E1/3: gateway "gateway-1" not seen as a neighbor`},
		},
		{
			name: "another neighbor on the gateway port",
			neighbors: map[string]map[string]apiutil.LLDPNeighborStatus{
				"leaf-1": {"E1/3": gw(apiutil.LLDPNeighbor{Name: "server-1", Port: "enp2s1"})},
			},
			errs: []string{`switch leaf-1: E1/3: gateway "gateway-1" not seen as a neighbor, but found: [server-1]`},
		},
		{
			name: "other connection types are not checked",
			neighbors: map[string]map[string]apiutil.LLDPNeighborStatus{
				"leaf-1": {
					"E1/1": {Type: apiutil.LLDPNeighborTypeServer, Expected: apiutil.LLDPNeighbor{Name: "server-1", Port: "enp2s1"}},
					"E1/2": {Type: apiutil.LLDPNeighborTypeFabric, Expected: apiutil.LLDPNeighbor{Name: "spine-1", Port: "E1/1"}},
				},
			},
		},
		{
			name: "port with nothing expected",
			neighbors: map[string]map[string]apiutil.LLDPNeighborStatus{
				"leaf-1": {"E1/3": {Type: apiutil.LLDPNeighborTypeGateway}},
			},
		},
		{
			name: "errors are ordered by switch and port",
			neighbors: map[string]map[string]apiutil.LLDPNeighborStatus{
				"leaf-2": {"E1/3": gw()},
				"leaf-1": {"E1/4": gw(), "E1/3": gw()},
			},
			errs: []string{
				`switch leaf-1: E1/3: gateway "gateway-1" not seen as a neighbor`,
				`switch leaf-1: E1/4: gateway "gateway-1" not seen as a neighbor`,
				`switch leaf-2: E1/3: gateway "gateway-1" not seen as a neighbor`,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			errs := gatewayLLDPErrors(&inspect.LLDPOut{Neighbors: test.neighbors})

			var got []string
			for _, err := range errs {
				got = append(got, err.Error())
			}

			require.Equal(t, test.errs, got)
		})
	}
}
