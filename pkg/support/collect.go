// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl/inspect"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/version"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var schemeBuilders = []*scheme.Builder{
	comp.CoreAPISchemeBuilder, comp.AppsAPISchemeBuilder,
	comp.HelmAPISchemeBuilder,
	comp.CMApiSchemeBuilder, comp.CMMetaSchemeBuilder,
	wiringapi.SchemeBuilder, vpcapi.SchemeBuilder, dhcpapi.SchemeBuilder, agentapi.SchemeBuilder,
	fabapi.SchemeBuilder,
}

func Collect(ctx context.Context, name string) (*Dump, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("getting hostname: %w", err)
	}

	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading /etc/os-release: %w", err)
	}

	kube, err := kubeutil.NewClient(ctx, "", schemeBuilders...)
	if err != nil {
		return nil, fmt.Errorf("creating kube client: %w", err)
	}

	slog.Info("Collecting all K8s resources (except secrets)")

	resources := &bytes.Buffer{}
	if err := dumpObjects(ctx, kube, kube.Scheme(), resources,
		fabapi.GroupVersion.WithKind(""),
		wiringapi.GroupVersion.WithKind(""),
		vpcapi.GroupVersion.WithKind(""),
		dhcpapi.GroupVersion.WithKind(""),
		agentapi.GroupVersion.WithKind(""),
		corev1.SchemeGroupVersion.WithKind("NodeList"),
		corev1.SchemeGroupVersion.WithKind("ConfigMapList"),
		corev1.SchemeGroupVersion.WithKind("ServiceList"),
		corev1.SchemeGroupVersion.WithKind("PodList"),
		appsv1.SchemeGroupVersion.WithKind("DeploymentList"),
		appsv1.SchemeGroupVersion.WithKind("DaemonSetList"),
	); err != nil {
		return nil, fmt.Errorf("dumping objects: %w", err)
	}

	now := kmetav1.Now()

	// TODO we need to clean up the dump from sensitive data

	// it's just a check
	{
		newKube, err := loadObjects(kube.Scheme(), bytes.NewReader(resources.Bytes()))
		if err != nil {
			return nil, fmt.Errorf("loading objects: %w", err)
		}

		kube = fake.NewClientBuilder().Build() // TODO: just to make sure we aren't using the original client

		{
			newF, newCNs, newNs, err := fab.GetFabAndNodes(ctx, newKube, fab.GetFabAndNodesOpts{
				// AllowNotHydrated: true, // TODO
			})
			if err != nil {
				return nil, fmt.Errorf("getting new fab and nodes: %w", err)
			}

			_ = newF
			_ = newCNs
			_ = newNs

			// spew.Dump(newF)
		}

		{
			if lldpOut, err := inspect.LLDP(ctx, newKube, inspect.LLDPIn{
				Strict:   true,
				Fabric:   true,
				External: true,
				Server:   true,
			}); err != nil {
				return nil, fmt.Errorf("inspecting lldp: %w", err)
			} else if err := inspect.Render(time.Now(), inspect.OutputTypeText, io.Discard, lldpOut); err != nil {
				return nil, fmt.Errorf("rendering lldp inspect: %w", err)
			}
		}
	}

	slog.Info("Collecting pod logs")

	podLogs, err := collectPodLogs(ctx)
	if err != nil {
		return nil, fmt.Errorf("collecting pod logs: %w", err)
	}

	return &Dump{
		DumpVersion:  DumpVersion,
		Name:         name,
		Time:         now,
		HHFabVersion: version.Version,
		Hostname:     hostname,
		OSRelease:    string(osRelease),
		Resources:    resources.Bytes(),
		PodLogs:      podLogs,
	}, nil
}
