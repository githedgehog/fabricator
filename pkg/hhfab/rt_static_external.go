// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errNoStaticExternalWithAttachment = errors.New("no static external with pre-configured attachment found")
	errNoVPCs                         = errors.New("no VPCs found")
)

// staticExternalPeeringTest tests static external connectivity using the External API
// with spec.static (non-BGP mode). This test requires a static External with an ExternalAttachment
// it creates the ExternalPeering dynamically to test VPC-to-External connectivity.
func staticExternalPeeringTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
	if testCtx.staticExtName == "" {
		return true, nil, errNoStaticExternalWithAttachment
	}

	// Verify the static external has at least one attachment configured
	extAttachList := &vpcapi.ExternalAttachmentList{}
	if err := testCtx.kube.List(ctx, extAttachList, kclient.MatchingLabels{
		vpcapi.LabelExternal: testCtx.staticExtName,
	}); err != nil {
		return false, nil, fmt.Errorf("listing external attachments: %w", err)
	}

	if len(extAttachList.Items) == 0 {
		return true, nil, fmt.Errorf("%w: external %s has no attachments", errNoStaticExternalWithAttachment, testCtx.staticExtName)
	}

	// Find an attachment with static config
	var staticAttach *vpcapi.ExternalAttachment
	for i := range extAttachList.Items {
		attach := &extAttachList.Items[i]
		if attach.Spec.Static != nil {
			staticAttach = attach

			break
		}
	}

	if staticAttach == nil {
		return true, nil, fmt.Errorf("%w: external %s has no static attachments", errNoStaticExternalWithAttachment, testCtx.staticExtName)
	}

	reverts := []RevertFunc{}

	// Get VPCs and create ExternalPeering for the first one
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, reverts, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) == 0 {
		return true, reverts, errNoVPCs
	}

	targetVPC := vpcs.Items[0].Name

	extPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	appendExtPeeringSpecByName(extPeerings, targetVPC, testCtx.staticExtName,
		[]string{"subnet-01"}, AllZeroPrefix)

	slog.Info("Creating static external peering", "vpc", targetVPC, "external", testCtx.staticExtName)
	if err := DoSetupPeerings(ctx, testCtx.kube, nil, extPeerings, nil, true); err != nil {
		return false, reverts, fmt.Errorf("setting up static external peering: %w", err)
	}

	// Add revert for the external peering
	peeringName := fmt.Sprintf("%s--%s", targetVPC, testCtx.staticExtName)
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Deleting ExternalPeering", "name", peeringName)
		peering := &vpcapi.ExternalPeering{
			ObjectMeta: kmetav1.ObjectMeta{
				Name:      peeringName,
				Namespace: kmetav1.NamespaceDefault,
			},
		}
		if err := kclient.IgnoreNotFound(testCtx.kube.Delete(ctx, peering)); err != nil {
			return fmt.Errorf("deleting external peering: %w", err)
		}

		return nil
	})

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, reverts, fmt.Errorf("testing static external connectivity: %w", err)
	}

	return false, reverts, nil
}
