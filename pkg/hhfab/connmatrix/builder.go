// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package connmatrix

import (
	"context"
	"fmt"
	"log/slog"

	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// MatrixBuilder composes a chain of ConnectivityProviders and produces a
// ConnectivityMatrix for a given set of endpoints.
type MatrixBuilder struct {
	kube      kclient.Reader
	providers []ConnectivityProvider
}

// NewMatrixBuilder returns a builder with the standard provider chain. The
// GatewayPeeringProvider is only included when gatewayEnabled is true, matching
// the existing IsSubnetReachable(checkGateway=...) semantics.
func NewMatrixBuilder(kube kclient.Reader, gatewayEnabled bool) *MatrixBuilder {
	providers := []ConnectivityProvider{
		&IntraVPCProvider{},
		&SwitchPeeringProvider{},
		&ExternalPeeringProvider{},
	}
	if gatewayEnabled {
		providers = append(providers, &GatewayPeeringProvider{})
	}

	return &MatrixBuilder{kube: kube, providers: providers}
}

// Build runs the provider chain and returns the assembled matrix.
func (b *MatrixBuilder) Build(ctx context.Context, endpoints []Endpoint) (*ConnectivityMatrix, error) {
	matrix := NewConnectivityMatrix(endpoints)
	for _, p := range b.providers {
		exps, err := p.BuildExpectations(ctx, b.kube, endpoints, matrix)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", p.Name(), err)
		}
		for _, e := range exps {
			matrix.Add(e)
		}
		slog.Debug("Provider ran", "name", p.Name(), "expectations", len(exps))
	}

	return matrix, nil
}
