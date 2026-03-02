// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
)

var (
	errNoStaticExternalWithAttachment = errors.New("no static external with pre-configured attachment found")
	errNoVPCs                         = errors.New("no VPCs found")
)

// staticExternalPeeringTest tests static external connectivity using the External API
// with spec.static. It creates the ExternalPeering dynamically to test VPC-to-External connectivity.
func (testCtx *VPCPeeringTestCtx) staticExternalPeeringTest(ctx context.Context) (bool, []RevertFunc, error) {
	if testCtx.staticExtName == "" {
		return true, nil, errNoStaticExternalWithAttachment
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) == 0 {
		return true, nil, errNoVPCs
	}

	targetVPC := vpcs.Items[0].Name

	extPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	appendExtPeeringSpecByName(extPeerings, targetVPC, testCtx.staticExtName,
		[]string{"subnet-01"}, AllZeroPrefix)

	slog.Info("Creating static external peering", "vpc", targetVPC, "external", testCtx.staticExtName)
	if err := DoSetupPeerings(ctx, testCtx.kube, nil, extPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up static external peering: %w", err)
	}

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing static external connectivity: %w", err)
	}

	return false, nil, nil
}
