// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"context"
	"fmt"
	"io"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func ValidateFabricGateway(ctx context.Context, l *Loader, fabricCfg *meta.FabricConfig) error {
	if l == nil {
		return fmt.Errorf("loader is nil") //nolint:goerr113
	}
	if fabricCfg == nil {
		return fmt.Errorf("fabric config is nil") //nolint:goerr113
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

	kube, err := defaultedCopy(ctx, kube)
	if err != nil {
		return err
	}

	if err := validateFabrics(ctx, kube, fabricCfg); err != nil {
		return fmt.Errorf("validating fabrics: %w", err)
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
		if err := gwGroup.Validate(ctx, kube, fabricCfg); err != nil {
			return fmt.Errorf("validating gateway group %q: %w", gwGroup.GetName(), err)
		}
	}

	gateways := &gwapi.GatewayList{}
	if err := kube.List(ctx, gateways); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}
	for _, gw := range gateways.Items {
		gw.Default()
		if err := gw.Validate(ctx, kube, fabricCfg); err != nil {
			return fmt.Errorf("validating gateway %q: %w", gw.GetName(), err)
		}
	}

	vpcInfos := &gwapi.VPCInfoList{}
	if err := kube.List(ctx, vpcInfos); err != nil {
		return fmt.Errorf("listing vpc infos: %w", err)
	}
	for _, vpcInfo := range vpcInfos.Items {
		vpcInfo.Default()
		if err := vpcInfo.Validate(ctx, kube, fabricCfg); err != nil {
			return fmt.Errorf("validating vpc info %q: %w", vpcInfo.GetName(), err)
		}
	}

	peerings := &gwapi.GatewayPeeringList{}
	if err := kube.List(ctx, peerings); err != nil {
		return fmt.Errorf("listing peerings: %w", err)
	}
	for _, peering := range peerings.Items {
		peering.Default()
		if err := peering.Validate(ctx, kube, fabricCfg); err != nil {
			return fmt.Errorf("validating peering %q: %w", peering.GetName(), err)
		}
	}

	return nil
}

// defaultedCopy returns the objects as admission stores them. Validators find related objects
// through the labels Default() sets, which the loaded wiring only gets from the webhooks on install.
func defaultedCopy(ctx context.Context, kube kclient.Reader) (kclient.Client, error) {
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, objList := range append([]kclient.ObjectList{&wiringapi.SwitchProfileList{}, &wiringapi.ServerProfileList{}}, printIncludeLists()...) {
		if err := kube.List(ctx, objList); err != nil {
			return nil, fmt.Errorf("listing %T: %w", objList, err)
		}
		for _, obj := range KubeListItems(objList) {
			obj = obj.DeepCopyObject().(kclient.Object) //nolint:forcetypeassert
			if defaultable, ok := obj.(interface{ Default() }); ok {
				defaultable.Default()
			}
			obj.SetResourceVersion("")
			builder = builder.WithObjects(obj)
		}
	}

	return builder.Build(), nil
}

// validateFabrics checks the wiring's Fabrics against each other and against Fabric/default, which
// the controller seeds from the config and so must not be in the wiring
func validateFabrics(ctx context.Context, kube kclient.Reader, fabricCfg *meta.FabricConfig) error {
	fabrics := &wiringapi.FabricList{}
	if err := kube.List(ctx, fabrics); err != nil {
		return fmt.Errorf("listing fabrics: %w", err)
	}

	withDefault := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&wiringapi.Fabric{
		ObjectMeta: kmetav1.ObjectMeta{Name: wiringapi.DefaultFabric, Namespace: kmetav1.NamespaceDefault},
		Spec:       wiringapi.DefaultFabricSpec(fabricCfg),
	})
	for _, fabric := range fabrics.Items {
		if fabric.Name == wiringapi.DefaultFabric {
			return fmt.Errorf("fabric %s is created from the config and must not be in the wiring", wiringapi.DefaultFabric) //nolint:goerr113
		}
		withDefault = withDefault.WithObjects(&fabric)
	}
	withDefaultKube := withDefault.Build()

	for _, fabric := range fabrics.Items {
		fabric.Default()
		if _, err := fabric.Validate(ctx, withDefaultKube, fabricCfg); err != nil {
			return fmt.Errorf("validating fabric %q: %w", fabric.Name, err)
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

// a function and not a var since listing fills the lists in, and validation may run concurrently
func printIncludeLists() []kclient.ObjectList {
	return []kclient.ObjectList{
		&wiringapi.FabricList{},
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
		&gwapi.GatewayPeeringList{},
	}
}

func PrintInclude(ctx context.Context, kube ReaderWithScheme, w io.Writer) error {
	if err := printKubeObjects(ctx, kube, w, printIncludeLists()...); err != nil {
		return fmt.Errorf("printing kube objects: %w", err)
	}

	return nil
}
