// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestGatewayExecSkipReason(t *testing.T) {
	waiting := func(name, reason string) corev1.PodStatus {
		return corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  name,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "msg"}},
		}}}
	}

	for name, tc := range map[string]struct {
		status corev1.PodStatus
		skip   string
	}{
		"crashloop":       {waiting("dataplane", "CrashLoopBackOff"), "dataplane container CrashLoopBackOff: msg"},
		"image pull":      {waiting("dataplane", "ImagePullBackOff"), "dataplane container ImagePullBackOff: msg"},
		"other container": {waiting("frr-exporter", "CrashLoopBackOff"), ""},
		"init container": {corev1.PodStatus{
			InitContainerStatuses: waiting("flush-vtep", "CrashLoopBackOff").ContainerStatuses,
			ContainerStatuses:     waiting("dataplane", "PodInitializing").ContainerStatuses,
		}, "init container flush-vtep CrashLoopBackOff: msg"},
		"init container running": {corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "flush-vtep", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
			ContainerStatuses:     waiting("dataplane", "PodInitializing").ContainerStatuses,
		}, ""},
		"container creating":  {waiting("dataplane", "ContainerCreating"), ""},
		"no container status": {corev1.PodStatus{}, ""},
		"running": {corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "dataplane",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}}, ""},
		"unschedulable": {corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type:    corev1.PodScheduled,
			Status:  corev1.ConditionFalse,
			Reason:  corev1.PodReasonUnschedulable,
			Message: "0/2 nodes are available: 1 Insufficient hugepages-1Gi",
		}}}, "pod unschedulable: 0/2 nodes are available: 1 Insufficient hugepages-1Gi"},
		"scheduling": {corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionFalse,
		}}}, ""},
	} {
		require.Equal(t, tc.skip, gatewayExecSkipReason(corev1.Pod{Status: tc.status}), name)
	}
}
