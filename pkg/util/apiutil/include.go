// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"context"
	"fmt"
	"io"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	gwmeta "go.githedgehog.com/gateway/api/meta"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func ValidateFabricGateway(ctx context.Context, l *Loader, fabricCfg *meta.FabricConfig, gwCfg *gwmeta.GatewayCtrlConfig) error {
	if l == nil {
		return fmt.Errorf("loader is nil") //nolint:goerr113
	}
	if fabricCfg == nil {
		return fmt.Errorf("fabric config is nil") //nolint:goerr113
	}
	if gwCfg == nil {
		return fmt.Errorf("gateway control config is nil") //nolint:goerr113
	}

	kube := l.kube

	// TODO refactor to make it a bit more generic and less repetitive

	profiles := switchprofile.NewDefaultSwitchProfiles()
	if err := profiles.RegisterAll(ctx, kube, fabricCfg); err != nil {
		return fmt.Errorf("registering default switch profiles for validation: %w", err)
	}

	if err := profiles.Enforce(ctx, kube, fabricCfg, false); err != nil {
		return fmt.Errorf("enforcing default switch profiles for validation: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &wiringapi.SwitchProfileList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating switch profiles: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &wiringapi.VLANNamespaceList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating vlan namespaces: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &wiringapi.SwitchGroupList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating switch groups: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &wiringapi.SwitchList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating switches: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &wiringapi.ServerList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating servers: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &wiringapi.ConnectionList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating connections: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &wiringapi.ServerProfileList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating server profiles: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &vpcapi.IPv4NamespaceList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating ipv4 namespaces: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &vpcapi.VPCList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating vpcs: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &vpcapi.VPCAttachmentList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating vpc attachments: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &vpcapi.VPCPeeringList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating vpc peerings: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &vpcapi.ExternalList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating externals: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &vpcapi.ExternalAttachmentList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating external attachments: %w", err)
	}

	if err := defaultAndValidate(ctx, kube, &vpcapi.ExternalPeeringList{}, fabricCfg); err != nil {
		return fmt.Errorf("validating external peerings: %w", err)
	}

	gwGroups := &gwapi.GatewayGroupList{}
	if err := kube.List(ctx, gwGroups); err != nil {
		return fmt.Errorf("listing gateway groups: %w", err)
	}
	for _, gwGroup := range gwGroups.Items {
		gwGroup.Default()
		if err := gwGroup.Validate(ctx, kube); err != nil {
			return fmt.Errorf("validating gateway group %q: %w", gwGroup.GetName(), err)
		}
	}

	gateways := &gwapi.GatewayList{}
	if err := kube.List(ctx, gateways); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}
	for _, gw := range gateways.Items {
		gw.Default()
		if err := gw.Validate(ctx, kube, gwCfg); err != nil {
			return fmt.Errorf("validating gateway %q: %w", gw.GetName(), err)
		}
	}

	vpcInfos := &gwapi.VPCInfoList{}
	if err := kube.List(ctx, vpcInfos); err != nil {
		return fmt.Errorf("listing vpc infos: %w", err)
	}
	for _, vpcInfo := range vpcInfos.Items {
		vpcInfo.Default()
		if err := vpcInfo.Validate(ctx, kube); err != nil {
			return fmt.Errorf("validating vpc info %q: %w", vpcInfo.GetName(), err)
		}
	}

	peerings := &gwapi.PeeringList{}
	if err := kube.List(ctx, peerings); err != nil {
		return fmt.Errorf("listing peerings: %w", err)
	}
	for _, peering := range peerings.Items {
		peering.Default()
		if err := peering.Validate(ctx, kube); err != nil {
			return fmt.Errorf("validating peering %q: %w", peering.GetName(), err)
		}
	}

	return nil
}

func defaultAndValidate(ctx context.Context, kube kclient.Reader, objList meta.ObjectList, cfg *meta.FabricConfig) error {
	if err := kube.List(ctx, objList); err != nil {
		return fmt.Errorf("listing %T: %w", objList, err)
	}

	for _, obj := range objList.GetItems() {
		obj.Default()
		if _, err := obj.Validate(ctx, kube, cfg); err != nil {
			return fmt.Errorf("validating %T %q: %w", obj, obj.GetName(), err)
		}
	}

	return nil
}

var printIncludeLists = []kclient.ObjectList{
	&wiringapi.VLANNamespaceList{},
	&vpcapi.IPv4NamespaceList{},
	&wiringapi.SwitchGroupList{},
	&wiringapi.SwitchList{},
	&wiringapi.ServerList{},
	&wiringapi.ConnectionList{},
	&vpcapi.ExternalList{},
	&vpcapi.ExternalAttachmentList{},
	&vpcapi.VPCList{},
	&vpcapi.VPCAttachmentList{},
	&vpcapi.VPCPeeringList{},
	&vpcapi.ExternalPeeringList{},
	&gwapi.GatewayGroupList{},
	&gwapi.GatewayList{},
	&gwapi.VPCInfoList{},
	&gwapi.PeeringList{},
}

func PrintInclude(ctx context.Context, kube ReaderWithScheme, w io.Writer) error {
	if err := printKubeObjects(ctx, kube, w, printIncludeLists...); err != nil {
		return fmt.Errorf("printing kube objects: %w", err)
	}

	return nil
}
