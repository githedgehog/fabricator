// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// Package v1beta1 contains API Schema definitions for the fabricator v1beta1 API group
// +kubebuilder:object:generate=true
// +groupName=fabricator.githedgehog.com
package v1beta1

import (
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "fabricator.githedgehog.com", Version: "v1beta1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		kmetav1.AddToGroupVersion(s, GroupVersion)

		return nil
	})

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
