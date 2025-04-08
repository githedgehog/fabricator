// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"fmt"

	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kyaml "sigs.k8s.io/yaml"
)

// v0: fully collectable through the K8s API
// - fab && nodes objects
// - wiring objects
// - gw objects
// - pods/logs/configmaps/etc

// Challenges:
// - how to cleanup everything from the sensitive data?
// - how to make it easy to use? kubectl not going to work with it but inspect commands and etc should

const (
	FileExt     = ".hhs"
	DumpVersion = "v0.0.0" // TODO use
)

var (
	ErrVersionMissing  = fmt.Errorf("dump version missing")
	ErrVersionMismatch = fmt.Errorf("dump version mismatch")
)

type Dump struct {
	DumpVersion  string                        `json:"version,omitempty"`
	Name         string                        `json:"name,omitempty"`
	Time         kmetav1.Time                  `json:"time,omitempty"`
	HHFabVersion string                        `json:"hhfabVersion,omitempty"`
	Hostname     string                        `json:"hostname,omitempty"`
	OSRelease    string                        `json:"osRelease,omitempty"`
	Resources    []byte                        `json:"resources,omitempty"`
	PodLogs      map[string]map[string]PodLogs `json:"podLogs,omitempty"`
}

type PodLogs map[string]ContainerLogs // container name -> logs

type ContainerLogs struct {
	Previous []byte `json:"previous,omitempty"`
	Current  []byte `json:"current,omitempty"`
}

func (d *Dump) Marshal() ([]byte, error) {
	data, err := kyaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshalling dump: %w", err)
	}

	return data, nil
}

func Unmarshal(data []byte) (*Dump, error) {
	d := &Dump{}
	if err := kyaml.UnmarshalStrict(data, d); err != nil {
		return nil, fmt.Errorf("unmarshalling dump: %w", err)
	}

	if d.DumpVersion == "" {
		return nil, ErrVersionMissing
	}
	if d.DumpVersion != DumpVersion {
		return nil, fmt.Errorf("dump version %q is not supported, expected %q:%w", d.DumpVersion, DumpVersion, ErrVersionMismatch)
	}

	return d, nil
}

// d.Fab, d.ControlNodes, d.Nodes, err = fab.GetFabAndNodes(ctx, kube, fab.GetFabAndNodesOpts{
// 	AllowNotHydrated: true, // TODO
// })
// if err != nil {
// 	return nil, fmt.Errorf("getting fab and nodes: %w", err)
// }
