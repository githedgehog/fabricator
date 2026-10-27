// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
)

func gwTestPod(name, uid, driver string, ready bool, terminating bool) corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}

	meta := kmetav1.ObjectMeta{Name: name, UID: ktypes.UID(uid)}
	if terminating {
		now := kmetav1.Now()
		meta.DeletionTimestamp = &now
	}

	return corev1.Pod{
		ObjectMeta: meta,
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "frr-exporter", Args: []string{"--driver", "ignored"}},
			{Name: gwDataplaneContainer, Args: []string{"--driver", driver}},
		}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
}

func TestGatewayPodsOnDriver(t *testing.T) {
	old := map[ktypes.UID]bool{"old": true}

	for _, tc := range []struct {
		name string
		pods []corev1.Pod
		done bool
		why  string
	}{
		{name: "no pods", why: "no pods"},
		{name: "only the old pod", pods: []corev1.Pod{gwTestPod("gw-a", "old", "kernel", true, false)}, why: "not replaced"},
		{name: "new pod not ready", pods: []corev1.Pod{gwTestPod("gw-b", "new", "dpdk", false, false)}, why: "not ready"},
		{name: "new pod on the wrong driver", pods: []corev1.Pod{gwTestPod("gw-b", "new", "kernel", true, false)}, why: `driver "kernel"`},
		{name: "new pod on the driver", pods: []corev1.Pod{gwTestPod("gw-b", "new", "dpdk", true, false)}, done: true},
		{
			name: "old pod still terminating",
			pods: []corev1.Pod{gwTestPod("gw-a", "old", "kernel", true, true), gwTestPod("gw-b", "new", "dpdk", true, false)},
			why:  "still terminating",
		},
		{name: "only a terminating pod", pods: []corev1.Pod{gwTestPod("gw-b", "new", "dpdk", true, true)}, why: "no pods"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done, why := gatewayPodsOnDriver(tc.pods, old, gwapi.GatewayDriverDPDK)
			require.Equal(t, tc.done, done)
			require.Contains(t, why, tc.why)
		})
	}
}

func TestLogDriver(t *testing.T) {
	dpdk := "INFO Initializing DPDK EAL with: --no-auto-probing --in-memory --lcores 0 --numa-mem=1024\n"
	kernel := "INFO Initializing DPDK EAL with: --no-auto-probing --in-memory --lcores 0 --no-huge --no-pci\n"

	require.Equal(t, gwapi.GatewayDriverDPDK, logDriver("INFO starting\n"+dpdk+"INFO ready\n"))
	require.Equal(t, gwapi.GatewayDriverKernel, logDriver("INFO starting\n"+kernel+"INFO ready\n"))
	require.Equal(t, gwapi.GatewayDriver(""), logDriver("INFO starting\nINFO ready\n"))
	require.Equal(t, gwapi.GatewayDriver(""), logDriver(""))
}

func TestGatewayPodCrash(t *testing.T) {
	old := map[ktypes.UID]bool{"old": true}

	restarted := func(name, uid string, terminating bool, restarts int32) corev1.Pod {
		pod := gwTestPod(name, uid, "dpdk", true, terminating)
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{Name: "frr-exporter"},
			{
				Name:                 gwDataplaneContainer,
				RestartCount:         restarts,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
			},
		}

		return pod
	}

	require.NoError(t, gatewayPodCrash(nil, old))
	require.NoError(t, gatewayPodCrash([]corev1.Pod{restarted("gw-b", "new", false, 0)}, old))
	require.NoError(t, gatewayPodCrash([]corev1.Pod{restarted("gw-a", "old", false, 3)}, old), "old pods are not checked")
	require.NoError(t, gatewayPodCrash([]corev1.Pod{restarted("gw-b", "new", true, 3)}, old), "terminating pods are not checked")

	require.EqualError(t, gatewayPodCrash([]corev1.Pod{restarted("gw-b", "new", false, 2)}, old),
		"container dataplane of pod gw-b restarted 2 times, last exit Error code 1")
}
