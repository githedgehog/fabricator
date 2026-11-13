// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package comp

import (
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	appsapi "k8s.io/api/apps/v1"
	coreapi "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetDeploymentStatus(t *testing.T) {
	const image = "registry/fabric:v1"

	progressing := []appsapi.DeploymentCondition{{Type: DeploymentProgressing, Status: ConditionTrue}}

	for _, tt := range []struct {
		name   string
		image  string
		gen    int64
		status appsapi.DeploymentStatus
		want   fabapi.ComponentStatus
	}{
		{
			name:  "rolled out",
			image: image,
			gen:   2,
			status: appsapi.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1, Conditions: progressing,
			},
			want: fabapi.CompStatusReady,
		},
		{
			name:  "old image",
			image: "registry/fabric:v0",
			gen:   2,
			status: appsapi.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1, Conditions: progressing,
			},
			want: fabapi.CompStatusPending,
		},
		{
			name:  "new generation not observed",
			image: image,
			gen:   3,
			status: appsapi.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1, Conditions: progressing,
			},
			want: fabapi.CompStatusPending,
		},
		{
			name:  "old pod still present",
			image: image,
			gen:   3,
			status: appsapi.DeploymentStatus{
				ObservedGeneration: 3, Replicas: 2, UpdatedReplicas: 1, AvailableReplicas: 1, Conditions: progressing,
			},
			want: fabapi.CompStatusPending,
		},
		{
			name:  "new pod not available",
			image: image,
			gen:   3,
			status: appsapi.DeploymentStatus{
				ObservedGeneration: 3, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 0, Conditions: progressing,
			},
			want: fabapi.CompStatusPending,
		},
		{
			name:  "no progressing condition",
			image: image,
			gen:   2,
			status: appsapi.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1,
			},
			want: fabapi.CompStatusPending,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			obj := &appsapi.Deployment{
				Name: "fabric-ctrl", Namespace: FabNamespace, Generation: tt.gen,
				Spec: appsapi.DeploymentSpec{
					Template: coreapi.PodTemplateSpec{
						Spec: coreapi.PodSpec{Containers: []coreapi.Container{{Name: "manager", Image: tt.image}}},
					},
				},
				Status: tt.status,
			}
			kube := fake.NewClientBuilder().WithObjects(obj).Build()

			status, err := GetDeploymentStatus("fabric-ctrl", "manager", image)(t.Context(), kube, fabapi.Fabricator{})
			require.NoError(t, err)
			require.Equal(t, tt.want, status)
		})
	}
}
