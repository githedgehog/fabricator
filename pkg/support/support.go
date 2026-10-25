// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"

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
	DumpVersion     `json:",inline"`
	Name            string                            `json:"name,omitempty"`            // Name of the dump
	CreatedBy       DumpCreator                       `json:"createdBy,omitempty"`       // Information about the creator of the dump
	CreatedAt       kmetav1.Time                      `json:"createdAt,omitempty"`       // Time when the dump was created
	Resources       string                            `json:"resources,omitempty"`       // Serialized resources
	PodLogs         map[string]map[string]PodLogs     `json:"podLogs,omitempty"`         // Logs for all running pods: namespace -> pod name -> container logs
	GatewayInsights map[string]map[string]ExecOutputs `json:"gatewayInsights,omitempty"` // Gateway insights: gateway-name -> exec-cmd -> outputs
}

type PodLogs map[string]ContainerLogs // Logs for all containers in the pod: container name -> logs

type ContainerLogs struct {
	Current  string `json:"current,omitempty"`
	Previous string `json:"previous,omitempty"`
}

type ExecOutputs struct {
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Error  string `json:"error,omitempty"`
}

// gzipMagic starts every gzip stream, while a YAML document can't start with it, so it tells the compressed dumps from
// the plain YAML ones made before
var gzipMagic = []byte{0x1f, 0x8b}

// Marshal writes the dump to w as gzip compressed YAML, as it's mostly the logs, which compress well. It's compressed
// on the way to w, so the compressed copy isn't kept in memory.
func Marshal(w io.Writer, d *Dump) error {
	data, err := kyaml.Marshal(d)
	if err != nil {
		return fmt.Errorf("marshalling dump: %w", err)
	}

	gz := gzip.NewWriter(w)
	if _, err := gz.Write(data); err != nil {
		return fmt.Errorf("compressing dump: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("compressing dump: %w", err)
	}

	return nil
}

// WriteFile marshals the dump into the file at path, replacing it if it exists
func WriteFile(path string, d *Dump, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("creating dump file: %w", err)
	}

	if err := Marshal(f, d); err != nil {
		_ = f.Close()
		_ = os.Remove(path)

		return err
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing dump file: %w", err)
	}

	return nil
}

// Unmarshal reads a dump made by Marshal or a plain YAML one made before the dumps were compressed
func Unmarshal(r io.Reader, d *Dump) error {
	br := bufio.NewReader(r)

	var in io.Reader = br
	if magic, err := br.Peek(len(gzipMagic)); err == nil && bytes.Equal(magic, gzipMagic) {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("decompressing dump: %w", err)
		}
		defer gz.Close()

		in = gz
	}

	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("reading dump: %w", err)
	}

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
