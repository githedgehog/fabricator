// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package f8r

import (
	"context"
	"fmt"
	"strings"

	"github.com/samber/lo"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/node"
	appsapi "k8s.io/api/apps/v1"
	coreapi "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	NodeConfigRef                      = "fabricator/hhfab-node-config"
	NodeConfigAirgapName               = "node-config-airgap-images-amd64.tar"
	NodeConfigDaemonSet                = "fab-node-config"
	NodeConfigDaemonSetConfigContainer = "config"
)

var _ comp.KubeInstall = InstallNodeConfig

func InstallNodeConfig(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	repo, err := comp.ImageURL(cfg, NodeConfigRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", NodeConfigRef, err)
	}

	image := repo + ":" + string(cfg.Status.Versions.Fabricator.NodeConfig)

	pauseImage := k3s.PauseImageURL + ":" + string(cfg.Status.Versions.Fabricator.Pause)

	labels := map[string]string{
		"app.kubernetes.io/name": NodeConfigDaemonSet,
	}

	return []kclient.Object{
		comp.NewDaemonSet(NodeConfigDaemonSet, appsapi.DaemonSetSpec{
			Selector: &kmetav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: coreapi.PodTemplateSpec{
				ObjectMeta: kmetav1.ObjectMeta{
					Labels: labels,
				},
				Spec: coreapi.PodSpec{
					Affinity: &coreapi.Affinity{
						NodeAffinity: &coreapi.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &coreapi.NodeSelector{
								NodeSelectorTerms: []coreapi.NodeSelectorTerm{
									{
										MatchExpressions: []coreapi.NodeSelectorRequirement{
											{
												Key:      "node-role.kubernetes.io/control-plane",
												Operator: coreapi.NodeSelectorOpDoesNotExist,
											},
											{
												Key:      "node-role.kubernetes.io/etcd",
												Operator: coreapi.NodeSelectorOpDoesNotExist,
											},
										},
									},
								},
							},
						},
					},
					Tolerations: lo.Map(fabapi.NodeRoles, func(role fabapi.FabNodeRole, _ int) coreapi.Toleration {
						return coreapi.Toleration{
							Key:      fabapi.RoleTaintKey(role),
							Operator: coreapi.TolerationOpExists,
							Effect:   coreapi.TaintEffectNoExecute,
						}
					}),
					HostPID:     true,
					HostNetwork: true,
					InitContainers: []coreapi.Container{
						{
							Name:            NodeConfigDaemonSetConfigContainer,
							Image:           image,
							ImagePullPolicy: coreapi.PullIfNotPresent,
							SecurityContext: &coreapi.SecurityContext{
								Privileged: ptr.To(true),
							},
							Command: []string{
								"/bin/sh",
								"-c",
								trimMultiline(`
									set -ex
									mkdir -p /proc/1/root/opt/hedgehog/node
									cp /bin/hhfab-node-config /proc/1/root/opt/hedgehog/node
									nsenter --mount=/proc/1/ns/mnt -- /opt/hedgehog/node/hhfab-node-config
								`),
							},
							Env: []coreapi.EnvVar{
								{
									Name: node.EnvNodeName,
									ValueFrom: &coreapi.EnvVarSource{
										FieldRef: &coreapi.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
								{
									Name: node.EnvCA,
									ValueFrom: &coreapi.EnvVarSource{
										ConfigMapKeyRef: &coreapi.ConfigMapKeySelector{
											LocalObjectReference: coreapi.LocalObjectReference{
												Name: comp.FabCAConfigMap,
											},
											Key: comp.FabCAConfigMapKey,
										},
									},
								},
								{
									Name: node.EnvRegistries,
									ValueFrom: &coreapi.EnvVarSource{
										SecretKeyRef: &coreapi.SecretKeySelector{
											LocalObjectReference: coreapi.LocalObjectReference{
												Name: comp.FabNodeRegistriesSecret,
											},
											Key: comp.FabNodeRegistriesSecretKey,
										},
									},
								},
								{
									Name:  node.EnvImage,
									Value: image,
								},
							},
						},
					},
					Containers: []coreapi.Container{
						{
							Name:  "wait",
							Image: pauseImage,
						},
					},
				},
			},
		}),
	}, nil
}

var _ comp.KubeStatus = StatusNodeConfig

func StatusNodeConfig(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	name := NodeConfigDaemonSet
	container := NodeConfigDaemonSetConfigContainer

	repo, err := comp.ImageURL(cfg, NodeConfigRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", NodeConfigRef, err)
	}

	image := repo + ":" + string(cfg.Status.Versions.Fabricator.NodeConfig)

	return comp.GetDaemonSetStatus(name, container, image)(ctx, kube, cfg)
}

func trimMultiline(s string) string {
	res := strings.Builder{}

	for line := range strings.Lines(s) {
		res.WriteString(strings.TrimSpace(line) + "\n")
	}

	return res.String()
}
