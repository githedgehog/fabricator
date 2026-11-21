// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.githedgehog.com/fabric/pkg/hhfctl/inspect"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
)

// gatewayLLDPErrorsOf is gatewayLLDPErrors for the output of the LLDP inspection, which has nothing to report for any
// other kind of output.
func gatewayLLDPErrorsOf(out inspect.Out[inspect.LLDPIn]) []error {
	lldpOut, ok := out.(*inspect.LLDPOut)
	if !ok || lldpOut == nil {
		return nil
	}

	return gatewayLLDPErrors(lldpOut)
}

// gatewayLLDPErrors reports every port wired to a gateway where the switch doesn't see that gateway as a neighbor.
// Only the advertised name is compared, the neighbor's port is not. Ports of other connection types are left to the
// LLDP inspection itself, which only warns about them as discovery isn't reliable on some switch platforms.
func gatewayLLDPErrors(out *inspect.LLDPOut) []error {
	var errs []error

	for _, swName := range slices.Sorted(maps.Keys(out.Neighbors)) {
		ports := out.Neighbors[swName]

		for _, port := range slices.Sorted(maps.Keys(ports)) {
			n := ports[port]
			if n.Type != apiutil.LLDPNeighborTypeGateway || n.Expected.Name == "" {
				continue
			}

			seen := make([]string, 0, len(n.Actual))
			found := false

			for _, actual := range n.Actual {
				if strings.EqualFold(actual.MatchedName(), n.Expected.Name) {
					found = true

					break
				}

				seen = append(seen, actual.MatchedName())
			}

			if found {
				continue
			}

			if len(seen) == 0 {
				errs = append(errs, fmt.Errorf("switch %s: %s: gateway %q not seen as a neighbor", swName, port, n.Expected.Name)) //nolint:err113
			} else {
				errs = append(errs, fmt.Errorf("switch %s: %s: gateway %q not seen as a neighbor, but found: %v", swName, port, n.Expected.Name, seen)) //nolint:err113
			}
		}
	}

	return errs
}
