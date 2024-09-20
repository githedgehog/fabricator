package apiutil

import (
	"context"
	"fmt"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ValidateFabric(ctx context.Context, l *Loader, fabricCfg *meta.FabricConfig) error {
	if l == nil {
		return fmt.Errorf("loader is nil") //nolint:goerr113
	}
	if fabricCfg == nil {
		return fmt.Errorf("fabric config is nil") //nolint:goerr113
	}

	kube := l.kube

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

	return nil
}

func defaultAndValidate(ctx context.Context, kube client.Reader, objList meta.ObjectList, cfg *meta.FabricConfig) error {
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
