// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package k3s

import (
	"crypto/sha256"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	appsapi "k8s.io/api/apps/v1"
	coreapi "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	LocalPathConfigMap = "local-path-config"
)

var _ comp.KubeInstall = InstallLocalPathConfig

// InstallLocalPathConfig creates the local-path-provisioner ConfigMap with tolerations
// for observability nodes so that PVCs can be provisioned on those nodes.
func InstallLocalPathConfig(_ fabapi.Fabricator) ([]kclient.Object, error) {
	configJSON := `{
  "nodePathMap":[
  {
    "node":"DEFAULT_PATH_FOR_NON_LISTED_NODES",
    "paths":["/var/lib/rancher/k3s/storage"]
  }
  ]
}`

	setupScript := `#!/bin/sh
set -eu
mkdir -m 0777 -p "${VOL_DIR}"
chmod 700 "${VOL_DIR}/.."`

	teardownScript := `#!/bin/sh
set -eu
if [ -d "${VOL_DIR}" ]; then
  rm -rf "${VOL_DIR}"
fi`

	helperPodYAML := `apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  tolerations:
  - key: role.fabricator.githedgehog.com/observability
    operator: Exists
    effect: NoExecute
  containers:
  - name: helper-pod
    image: "rancher/mirrored-library-busybox:1.36.1"
    imagePullPolicy: IfNotPresent`

	// Calculate checksum of the helperPod.yaml to force deployment update when it changes
	h := sha256.New()
	h.Write([]byte(helperPodYAML))
	checksum := fmt.Sprintf("%x", h.Sum(nil))[:16]

	configMap := &coreapi.ConfigMap{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      LocalPathConfigMap,
			Namespace: "kube-system",
			Annotations: map[string]string{
				"fabricator.githedgehog.com/helper-pod-checksum": checksum,
			},
		},
		Data: map[string]string{
			"config.json":    configJSON,
			"setup":          setupScript,
			"teardown":       teardownScript,
			"helperPod.yaml": helperPodYAML,
		},
	}

	return []kclient.Object{configMap}, nil
}

// InstallLocalPathProvisionerPatch patches the local-path-provisioner deployment
// to trigger a restart when the ConfigMap changes by adding the checksum annotation
func InstallLocalPathProvisionerPatch(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	// Get the checksum that we computed for the ConfigMap to force restart when it changes
	h := sha256.New()
	helperPodYAML := `apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  tolerations:
  - key: role.fabricator.githedgehog.com/observability
    operator: Exists
    effect: NoExecute
  containers:
  - name: helper-pod
    image: "rancher/mirrored-library-busybox:1.36.1"
    imagePullPolicy: IfNotPresent`
	h.Write([]byte(helperPodYAML))
	checksum := fmt.Sprintf("%x", h.Sum(nil))[:16]

	// Create a Deployment patch that adds annotation to pod template
	// This will trigger a rolling update, causing pods to restart and reload the ConfigMap
	replicas := int32(1)
	deployment := &appsapi.Deployment{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      "local-path-provisioner",
			Namespace: "kube-system",
		},
		Spec: appsapi.DeploymentSpec{
			Replicas: &replicas,
			Selector: &kmetav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "local-path-provisioner",
				},
			},
			Template: coreapi.PodTemplateSpec{
				ObjectMeta: kmetav1.ObjectMeta{
					Labels: map[string]string{
						"app": "local-path-provisioner",
					},
					Annotations: map[string]string{
						"fabricator.githedgehog.com/config-checksum": checksum,
					},
				},
				Spec: coreapi.PodSpec{
					ServiceAccountName: "local-path-provisioner-service-account",
					NodeSelector: map[string]string{
						"kubernetes.io/os": "linux",
					},
					PriorityClassName: "system-node-critical",
					Tolerations: []coreapi.Toleration{
						{
							Key:      "CriticalAddonsOnly",
							Operator: coreapi.TolerationOpExists,
						},
						{
							Key:      "node-role.kubernetes.io/control-plane",
							Operator: coreapi.TolerationOpExists,
							Effect:   coreapi.TaintEffectNoSchedule,
						},
					},
					Containers: []coreapi.Container{
						{
							Name:  "local-path-provisioner",
							Image: "rancher/local-path-provisioner:v0.0.32",
							Command: []string{
								"local-path-provisioner",
								"start",
								"--config",
								"/etc/config/config.json",
							},
							Env: []coreapi.EnvVar{
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &coreapi.EnvVarSource{
										FieldRef: &coreapi.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
							},
							VolumeMounts: []coreapi.VolumeMount{
								{
									Name:      "config-volume",
									MountPath: "/etc/config/",
								},
							},
						},
					},
					Volumes: []coreapi.Volume{
						{
							Name: "config-volume",
							VolumeSource: coreapi.VolumeSource{
								ConfigMap: &coreapi.ConfigMapVolumeSource{
									LocalObjectReference: coreapi.LocalObjectReference{
										Name: LocalPathConfigMap,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return []kclient.Object{deployment}, nil
}
