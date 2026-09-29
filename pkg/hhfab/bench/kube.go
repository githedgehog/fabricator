// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	coreapi "k8s.io/api/core/v1"
	rbacapi "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultQPS and DefaultBurst replace client-go's defaults of 5 and 10, which
// would otherwise throttle the loader well below what the apiserver can take
// and make the bench measure its own rate limiter.
const (
	DefaultQPS   = 200
	DefaultBurst = 400
)

// benchScheme is the scheme every bench client uses.
func benchScheme() (*runtime.Scheme, error) {
	scheme, err := kubeutil.NewScheme(schemeBuilders...)
	if err != nil {
		return nil, fmt.Errorf("creating scheme: %w", err)
	}

	return scheme, nil
}

var schemeBuilders = []func(*runtime.Scheme) error{
	wiringapi.AddToScheme,
	vpcapi.AddToScheme,
	agentapi.AddToScheme,
	fabapi.AddToScheme,
	coreapi.AddToScheme,
	rbacapi.AddToScheme,
}

// NewKubeClient builds an uncached client against the given kubeconfig with
// the rate limits raised.
//
// The bench deliberately does not use a cached client: an informer would LIST
// and WATCH every object up front, including every Agent with its full status,
// which is pure overhead for a writer and would make the driver's own memory
// the thing that breaks first.
func NewKubeClient(ctx context.Context, kubeconfig string, qps float32, burst int) (kclient.WithWatch, error) {
	cfg, err := kubeutil.NewClientConfig(ctx, kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("creating kube config: %w", err)
	}

	cfg.QPS = qps
	cfg.Burst = burst

	scheme, err := benchScheme()
	if err != nil {
		return nil, err
	}

	kube, err := kclient.NewWithWatch(cfg, kclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating kube client: %w", err)
	}

	return kube, nil
}
