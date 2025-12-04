// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"errors"
	"fmt"

	"github.com/Masterminds/semver/v3"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kyaml "sigs.k8s.io/yaml"
)

const (
	FileExt = ".hhs"
)

var (
	CurrentVersion   = mustVersion("0.1.0")
	SupportedVersion = mustConstraint("~0.1")
)

var (
	ErrVersionMissing  = fmt.Errorf("support dump version missing")
	ErrVersionMismatch = fmt.Errorf("support dump version mismatch")
)

type DumpVersion struct {
	Version       string `json:"version"` // Support dump API version, regular semver expectations
	parsedVersion *semver.Version
}

type DumpCreator struct {
	Hostname   string `json:"hostname,omitempty"`   // Hostname of the machine where the dump was created
	Username   string `json:"username,omitempty"`   // Username of the user who created the dump
	OSRelease  string `json:"osRelease,omitempty"`  // OS information (/etc/os-release)
	CtlVersion string `json:"ctlVersion,omitempty"` // Version of the hhfab/hhfabctl binary that created the dump
}

type Dump struct {
	DumpVersion `json:",inline"`
	Name        string                        `json:"name,omitempty"`      // Name of the dump
	CreatedBy   DumpCreator                   `json:"createdBy,omitempty"` // Information about the creator of the dump
	CreatedAt   kmetav1.Time                  `json:"createdAt,omitempty"` // Time when the dump was created
	Resources   string                        `json:"resources,omitempty"` // Serialized resources
	PodLogs     map[string]map[string]PodLogs `json:"podLogs,omitempty"`   // Logs for all running pods: namespace -> pod name -> container logs
}

type PodLogs map[string]ContainerLogs // Logs for all containers in the pod: container name -> logs

type ContainerLogs struct {
	Current  string `json:"current,omitempty"`
	Previous string `json:"previous,omitempty"`
}

func Marshal(d *Dump) ([]byte, error) {
	data, err := kyaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshalling dump: %w", err)
	}

	return data, nil
}

func Unmarshal(data []byte, d *Dump) error {
	dv := &DumpVersion{}
	if err := kyaml.Unmarshal(data, dv); err != nil {
		return fmt.Errorf("unmarshalling dump version: %w", err)
	}

	if dv.Version == "" {
		return ErrVersionMissing
	}

	dumpVersion, err := semver.NewVersion(dv.Version)
	if err != nil {
		return fmt.Errorf("parsing dump version: %w", err)
	}

	if ok, errs := SupportedVersion.Validate(dumpVersion); !ok {
		return fmt.Errorf("dump version %q is not supported: %w", dv.Version, errors.Join(errs...))
	}

	if err := kyaml.UnmarshalStrict(data, d); err != nil {
		return fmt.Errorf("unmarshalling dump: %w", err)
	}

	d.parsedVersion = dumpVersion

	return nil
}

func mustVersion(version string) *semver.Version {
	v, err := semver.NewVersion(version)
	if err != nil {
		panic(fmt.Sprintf("parsing version %q: %v", version, err))
	}

	return v
}

func mustConstraint(constraint string) *semver.Constraints {
	c, err := semver.NewConstraint(constraint)
	if err != nil {
		panic(fmt.Sprintf("parsing constraint %q: %v", constraint, err))
	}

	return c
}
