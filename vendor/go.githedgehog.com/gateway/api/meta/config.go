// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package meta

import (
	corev1 "k8s.io/api/core/v1"
)

type GatewayCtrlConfig struct {
	Namespace            string              `json:"namespace,omitempty"`
	Tolerations          []corev1.Toleration `json:"tolerations,omitempty"`
	AgentRef             string              `json:"agentRef,omitempty"`
	DataplaneRef         string              `json:"dataplaneRef,omitempty"`
	FRRRef               string              `json:"frrRef,omitempty"`
	DataplaneMetricsPort uint16              `json:"dataplaneMetricsPort,omitempty"`
	FRRMetricsPort       uint16              `json:"frrMetricsPort,omitempty"`
}

type AgentConfig struct {
	Name             string `json:"name,omitempty"`
	Namespace        string `json:"namespace,omitempty"`
	DataplaneAddress string `json:"dataplaneAddress,omitempty"`
}
