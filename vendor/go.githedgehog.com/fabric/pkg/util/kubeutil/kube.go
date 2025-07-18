// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kubeutil

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientcache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	kctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

func NewClient(ctx context.Context, kubeconfigPath string, schemeBuilders ...*scheme.Builder) (kclient.WithWatch, error) {
	_, kube, err := newClient(ctx, kubeconfigPath, false, false, schemeBuilders...)

	return kube, err
}

func NewClientWithCore(ctx context.Context, kubeconfigPath string, schemeBuilders ...*scheme.Builder) (kclient.WithWatch, error) {
	_, kube, err := newClient(ctx, kubeconfigPath, true, false, schemeBuilders...)

	return kube, err
}

func NewClientWithCache(ctx context.Context, kubeconfigPath string, schemeBuilders ...*scheme.Builder) (context.CancelFunc, kclient.WithWatch, error) {
	return newClient(ctx, kubeconfigPath, false, true, schemeBuilders...)
}

// TODO cached version is minimal naive implementation with hanging go routine, need to be improved
func newClient(ctx context.Context, kubeconfigPath string, core, cached bool, schemeBuilders ...*scheme.Builder) (context.CancelFunc, kclient.WithWatch, error) { //nolint:contextcheck
	cancel := func() {}
	cfg, err := NewClientConfig(ctx, kubeconfigPath)
	if err != nil {
		return cancel, nil, errors.Wrapf(err, "failed to create kube config")
	}

	scheme, err := NewScheme(schemeBuilders...)
	if err != nil {
		return cancel, nil, errors.Wrapf(err, "failed to create scheme")
	}

	if core {
		if err := corev1.AddToScheme(scheme); err != nil {
			return cancel, nil, errors.Wrapf(err, "failed to add core scheme to runtime")
		}
	}

	var cacheOpts *kclient.CacheOptions
	if cached {
		clientCache, err := cache.New(cfg, cache.Options{
			Scheme:                   scheme,
			DefaultWatchErrorHandler: cacheWatchErrorHandler,
		})
		if err != nil {
			return cancel, nil, errors.Wrapf(err, "failed to create kube controller runtime cache")
		}

		// Use a separate context for the cache to avoid canceling when parent context is canceled
		var cacheCtx context.Context
		cacheCtx, cancel = context.WithCancel(context.Background())

		go func() {
			if err := clientCache.Start(cacheCtx); err != nil {
				slog.Error("failed to start kube controller runtime cache", "err", err)
				panic(fmt.Errorf("failed to start kube controller runtime cache: %w", err))
			}
		}()

		if !clientCache.WaitForCacheSync(ctx) {
			return cancel, nil, errors.New("failed to sync kube controller runtime cache")
		}

		cacheOpts = &kclient.CacheOptions{
			Reader: clientCache,
		}
	}

	kubeClient, err := kclient.NewWithWatch(cfg, kclient.Options{
		Scheme: scheme,
		Cache:  cacheOpts,
	})
	if err != nil {
		return cancel, nil, errors.Wrapf(err, "failed to create kube controller runtime client")
	}

	return cancel, kubeClient, nil
}

func cacheWatchErrorHandler(ctx context.Context, r *clientcache.Reflector, err error) {
	switch {
	case kapierrors.IsResourceExpired(err) || kapierrors.IsGone(err):
		clientcache.DefaultWatchErrorHandler(ctx, r, err)
	case errors.Is(err, io.EOF):
		// watch closed normally
	case errors.Is(err, io.ErrUnexpectedEOF):
		clientcache.DefaultWatchErrorHandler(ctx, r, err)
	default:
		slog.Error("kube controller runtime cache: failed to watch", "err", err)
		clientcache.DefaultWatchErrorHandler(ctx, r, err)
		panic(fmt.Errorf("kube controller runtime cache: failed to watch: %w", err))
	}
}

func NewClientConfig(ctx context.Context, kubeconfigPath string) (*rest.Config, error) {
	var cfg *rest.Config
	var err error

	if kubeconfigPath == "" {
		if cfg, err = kctrl.GetConfig(); err != nil {
			return nil, fmt.Errorf("getting kubeconfig using default path or in-cluster config: %w", err)
		}
	} else {
		if cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
			nil,
		).ClientConfig(); err != nil {
			return nil, fmt.Errorf("loading kubeconfig from %s: %w", kubeconfigPath, err)
		}
	}

	return cfg, nil
}

func NewScheme(schemeBuilders ...*scheme.Builder) (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	for _, schemeBuilder := range schemeBuilders {
		if err := schemeBuilder.AddToScheme(scheme); err != nil {
			return nil, fmt.Errorf("adding scheme %s to runtime: %w", schemeBuilder.GroupVersion.String(), err)
		}
	}

	return scheme, nil
}

func NewClientset(ctx context.Context, kubeconfigPath string) (*kubernetes.Clientset, error) {
	cfg, err := NewClientConfig(ctx, kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("creating kube config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	return clientset, nil
}
