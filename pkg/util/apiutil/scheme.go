// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"fmt"

	"github.com/samber/lo"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var FabricatorGVKs = []schema.GroupVersionKind{
	fabapi.GroupVersion.WithKind(""),
}

var FabricGVKs = []schema.GroupVersionKind{
	wiringapi.GroupVersion.WithKind(""),
	vpcapi.GroupVersion.WithKind(""),
}

var FabricSkipPrintGVKs = []schema.GroupVersionKind{
	wiringapi.GroupVersion.WithKind("SwitchProfile"),
}

var GatewayGVKs = []schema.GroupVersionKind{
	gwapi.GroupVersion.WithKind(""),
}

var FabricGatewayGVKs = lo.Flatten(
	[][]schema.GroupVersionKind{
		FabricGVKs,
		GatewayGVKs,
	},
)

var (
	scheme  = runtime.NewScheme()
	decoder runtime.Decoder
)

func init() {
	if err := wiringapi.AddToScheme(scheme); err != nil {
		panic(fmt.Errorf("adding wiringapi to the scheme: %w", err))
	}
	if err := vpcapi.AddToScheme(scheme); err != nil {
		panic(fmt.Errorf("adding vpcapi to the scheme: %w", err))
	}
	if err := fabapi.AddToScheme(scheme); err != nil {
		panic(fmt.Errorf("adding fabapi to the scheme: %w", err))
	}
	if err := gwapi.AddToScheme(scheme); err != nil {
		panic(fmt.Errorf("adding gwapi to the scheme: %w", err))
	}
	decoder = serializer.NewCodecFactory(scheme, serializer.EnableStrict).UniversalDeserializer()
}
