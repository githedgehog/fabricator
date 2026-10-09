// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"log/slog"
	"strings"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// measureNamespaceDeleteDenial is a temporary measurement, not meant to be merged: it creates a namespace with
// one VPC, deletes the VPC and records how long until the VPC is gone and until the namespace delete is accepted.
func measureNamespaceDeleteDenial(ctx context.Context, kube kclient.Client, mode vpcapi.VPCMode, vlan uint16, rounds int) {
	const (
		nsName  = "measure-ns"
		vpcName = "vpc-measure"
		poll    = 10 * time.Millisecond
	)

	for round := 1; round <= rounds; round++ {
		ns := &vpcapi.IPv4Namespace{
			TypeMeta:   kmetav1.TypeMeta{Kind: vpcapi.KindIPv4Namespace, APIVersion: vpcapi.GroupVersion.String()},
			ObjectMeta: kmetav1.ObjectMeta{Name: nsName, Namespace: kmetav1.NamespaceDefault},
			Spec:       vpcapi.IPv4NamespaceSpec{Subnets: []string{"10.200.0.0/16"}},
		}
		vpc := &vpcapi.VPC{
			TypeMeta:   kmetav1.TypeMeta{Kind: vpcapi.KindVPC, APIVersion: vpcapi.GroupVersion.String()},
			ObjectMeta: kmetav1.ObjectMeta{Name: vpcName, Namespace: kmetav1.NamespaceDefault},
			Spec: vpcapi.VPCSpec{
				Mode:          mode,
				IPv4Namespace: nsName,
				VLANNamespace: "default",
				Subnets: map[string]*vpcapi.VPCSubnet{
					"measure-sub": {Subnet: "10.200.1.0/24", VLAN: vlan, DHCP: vpcapi.VPCDHCP{Enable: true}},
				},
			},
		}

		if err := kube.Create(ctx, ns); err != nil {
			slog.Warn("Measure: creating namespace", "round", round, "err", err)

			return
		}
		if err := kube.Create(ctx, vpc); err != nil {
			slog.Warn("Measure: creating VPC", "round", round, "err", err)
			_ = kube.Delete(ctx, ns)

			return
		}

		// let the controllers settle like in a real test where the VPC lives for a while
		time.Sleep(5 * time.Second)

		start := time.Now()
		if err := kube.Delete(ctx, vpc); err != nil {
			slog.Warn("Measure: deleting VPC", "round", round, "err", err)

			return
		}

		var vpcGone time.Duration
		_ = wait.PollUntilContextTimeout(ctx, poll, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			gone := kapierrors.IsNotFound(kube.Get(ctx, kclient.ObjectKeyFromObject(vpc), &vpcapi.VPC{}))
			if gone {
				vpcGone = time.Since(start)
			}

			return gone, nil
		})

		denied := 0
		var lastErr error
		_ = wait.PollUntilContextTimeout(ctx, poll, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			lastErr = kclient.IgnoreNotFound(kube.Delete(ctx, ns))
			if lastErr != nil && strings.Contains(lastErr.Error(), "IPv4Namespace has VPCs") {
				denied++

				return false, nil
			}

			return true, nil
		})
		nsDone := time.Since(start)

		slog.Info("Measure: overlap namespace delete", "round", round, "vpcGone", vpcGone, "nsDone", nsDone, "denied", denied, "err", lastErr)
		if lastErr != nil {
			return
		}
	}
}
