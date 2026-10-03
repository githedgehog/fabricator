// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ignitionFile returns the decoded contents of the file at path in an
// ignition config, failing if it is not there.
func ignitionFile(t *testing.T, ign []byte, path string) string {
	t.Helper()

	cfg := struct {
		Storage struct {
			Files []struct {
				Path     string `json:"path"`
				Contents struct {
					Source      string `json:"source"`
					Compression string `json:"compression"`
				} `json:"contents"`
			} `json:"files"`
		} `json:"storage"`
	}{}
	require.NoError(t, json.Unmarshal(ign, &cfg))

	for _, file := range cfg.Storage.Files {
		if file.Path != path {
			continue
		}

		// Butane writes either "data:,<url-escaped>" or "data:;base64,<base64>".
		header, payload, found := strings.Cut(file.Contents.Source, ",")
		require.True(t, found, "contents of %s are not a data URL", path)

		var raw []byte
		if strings.HasSuffix(header, ";base64") {
			var err error
			raw, err = base64.StdEncoding.DecodeString(payload)
			require.NoError(t, err)
		} else {
			unescaped, err := url.PathUnescape(payload)
			require.NoError(t, err)
			raw = []byte(unescaped)
		}

		if file.Contents.Compression == "gzip" {
			gz, err := gzip.NewReader(bytes.NewReader(raw))
			require.NoError(t, err)
			raw, err = io.ReadAll(gz)
			require.NoError(t, err)
		}

		return string(raw)
	}

	t.Fatalf("ignition has no file %s", path)

	return ""
}

func testFab() fabapi.Fabricator {
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Spec.Config.Control.VIP = "172.30.0.1/32"

	return f
}

// Every install gets the VLAB-only virtio naming rule, byte for byte, so the
// same file is in place whether the machine was installed or upgraded.
func TestIgnitionHasVLABVirtioNames(t *testing.T) {
	t.Parallel()

	mgmt := fabapi.ControlNodeManagement{IP: meta.Prefix("172.30.0.5/21"), Interface: "enp2s1"}
	dummy := fabapi.ControlNodeDummy{IP: meta.Prefix("172.30.90.0/31")}

	control := &ControlInstallBuilder{
		Fab: testFab(),
		Control: fabapi.ControlNode{
			ObjectMeta: kmetav1.ObjectMeta{Name: "control-1"},
			Spec: fabapi.ControlNodeSpec{
				Management: mgmt,
				External:   fabapi.ControlNodeExternal{IP: meta.PrefixOrDHCP("dhcp"), Interface: "enp2s0"},
				Dummy:      dummy,
			},
		},
		Mode: BuildModeManual,
	}
	ign, err := control.buildIgnition()
	require.NoError(t, err)
	require.Equal(t, flatcar.VLABVirtioNamesLink, ignitionFile(t, ign, flatcar.VLABVirtioNamesLinkPath))

	node := &NodeInstallBuilder{
		Fab: testFab(),
		Node: fabapi.FabNode{
			ObjectMeta: kmetav1.ObjectMeta{Name: "gateway-1"},
			Spec: fabapi.FabNodeSpec{
				Roles:      []fabapi.FabNodeRole{fabapi.NodeRoleGateway},
				Management: mgmt,
				Dummy:      dummy,
			},
		},
		Mode: BuildModeManual,
	}
	ign, err = node.buildIgnition()
	require.NoError(t, err)
	require.Equal(t, flatcar.VLABVirtioNamesLink, ignitionFile(t, ign, flatcar.VLABVirtioNamesLinkPath))
}

// The rule has to stay inert anywhere but VLAB: it may only ever match a
// virtio NIC on a machine carrying the VLAB asset tag, and must sort before
// Flatcar's 98-virtio.link to take effect there.
func TestVLABVirtioNamesOnlyMatchVLAB(t *testing.T) {
	t.Parallel()

	link := flatcar.VLABVirtioNamesLink
	match := link[strings.Index(link, "[Match]"):strings.Index(link, "[Link]")]

	require.Contains(t, match, "Driver=virtio_net\n")
	require.Contains(t, match, "Firmware=smbios-field(chassis_asset_tag = "+flatcar.VLABAssetTag+")\n")
	// Only the first matching .link file applies: after the explicit VLAB server
	// renames and the e1000 offloads, before Flatcar's own virtio naming.
	name := filepath.Base(flatcar.VLABVirtioNamesLinkPath)
	require.Greater(t, name, "10-rename-enp2s2.link")
	require.Greater(t, name, "10-e1000-offloads.link")
	require.Less(t, name, "98-gce-coreos-virtio.link")
	require.Less(t, name, "98-virtio.link")
}

func TestInstallVLABVirtioNamesIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), filepath.Base(flatcar.VLABVirtioNamesLinkPath))

	changed, err := updateConfigFile(path, flatcar.VLABVirtioNamesLink)
	require.NoError(t, err)
	require.True(t, changed)

	changed, err = updateConfigFile(path, flatcar.VLABVirtioNamesLink)
	require.NoError(t, err)
	require.False(t, changed)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, flatcar.VLABVirtioNamesLink, string(got))
}
