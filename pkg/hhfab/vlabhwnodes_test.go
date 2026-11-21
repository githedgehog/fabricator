// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

func TestBMCNodes(t *testing.T) {
	c := &Config{
		Nodes: []fabapi.FabNode{
			{Name: "gateway-1", Annotations: map[string]string{HHFabCfgBMC: "https://10.0.0.1"}},
			{Name: "gateway-bm", Annotations: map[string]string{HHFabCfgType: HHFabCfgTypeHW, HHFabCfgBMC: "https://10.0.0.2"}},
			{Name: "gateway-hw", Annotations: map[string]string{HHFabCfgType: HHFabCfgTypeHW}},
		},
	}

	nodes, err := c.bmcNodes()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"gateway-bm": "https://10.0.0.2"}, nodes)

	c.Nodes[1].Annotations[HHFabCfgBMC] = "https://10.0.0.2/"
	nodes, err = c.bmcNodes()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"gateway-bm": "https://10.0.0.2"}, nodes)

	for _, endpoint := range []string{"10.0.0.2", "ftp://10.0.0.2", "https://", "://bad", "https://10.0.0.2/redfish/v1", "https://10.0.0.2?x=1"} {
		c.Nodes[1].Annotations[HHFabCfgBMC] = endpoint
		_, err := c.bmcNodes()
		require.Error(t, err, endpoint)
	}
}

func TestSelectBMCNodes(t *testing.T) {
	c := &Config{
		Nodes: []fabapi.FabNode{
			{Name: "gateway-1"},
			{Name: "gateway-bm", Annotations: map[string]string{HHFabCfgType: HHFabCfgTypeHW, HHFabCfgBMC: "https://10.0.0.2"}},
			{Name: "gateway-hw", Annotations: map[string]string{HHFabCfgType: HHFabCfgTypeHW}},
		},
	}

	nodes, err := c.selectBMCNodes(nil, false)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"gateway-bm": "https://10.0.0.2"}, nodes)

	nodes, err = c.selectBMCNodes([]string{"gateway-bm"}, false)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"gateway-bm": "https://10.0.0.2"}, nodes)

	for _, name := range []string{"gateway-1", "gateway-hw", "gateway-2"} {
		_, err := c.selectBMCNodes([]string{name}, false)
		require.Error(t, err, name)
	}

	_, err = (&Config{}).selectBMCNodes(nil, false)
	require.Error(t, err)

	// an unused node's bad endpoint is reported, not taken as no BMC
	c.UnusedHW.Nodes = []fabapi.FabNode{{Name: "gateway-off", Annotations: map[string]string{HHFabCfgType: HHFabCfgTypeHW, HHFabCfgBMC: "https://10.0.0.3/redfish/v1"}}}
	_, err = c.selectBMCNodes([]string{"gateway-off"}, true)
	require.ErrorContains(t, err, "should have no path")
}

func TestCheckBMCNodes(t *testing.T) {
	nodes := map[string]string{"gateway-bm": "https://127.0.0.1"}
	opts := BMCOpts{Username: "admin", Password: "password"}

	require.NoError(t, checkBMCNodes(t.Context(), nil, BMCOpts{}))
	require.NoError(t, checkBMCNodes(t.Context(), nodes, opts))

	noPassword := opts
	noPassword.Password = ""
	require.ErrorIs(t, checkBMCNodes(t.Context(), nodes, noPassword), ErrBMCCredentials)

	require.Error(t, checkBMCNodes(t.Context(), map[string]string{"gateway-bm": "https://192.0.2.1"}, opts))
}

func TestCheckClusterVersion(t *testing.T) {
	c := &Config{WorkDir: t.TempDir()}
	// what this hhfab would build doesn't matter, only the installer in the result dir does
	c.Fab.Status.Versions.Fabricator.NodeConfig = "v1.2.4"

	cluster := &fabapi.Fabricator{}
	cluster.Status.Versions.Fabricator.NodeConfig = "v1.2.3-dirty-Lv2309"
	require.ErrorContains(t, c.checkClusterVersion(cluster, "gateway-bm"), "reading node installer config")

	// written as the node installer build does
	installer := fabapi.Fabricator{Name: comp.FabName, Namespace: comp.FabNamespace}
	installer.Status.Versions.Fabricator.NodeConfig = "v1.2.3-dirty-Lv2309"
	node := fabapi.FabNode{Name: "gateway-bm", Namespace: comp.FabNamespace}
	installDir := filepath.Join(c.WorkDir, ResultDir, string(recipe.TypeNode)+recipe.Separator+"gateway-bm"+recipe.Separator+recipe.InstallSuffix)
	require.NoError(t, os.MkdirAll(installDir, 0o700))
	buf := &bytes.Buffer{}
	require.NoError(t, apiutil.PrintFab(installer, nil, []fabapi.FabNode{node}, apiutil.NewLoader().GetClient().Scheme(), buf))
	require.NoError(t, os.WriteFile(filepath.Join(installDir, recipe.FabName), buf.Bytes(), 0o600))

	require.NoError(t, c.checkClusterVersion(cluster, "gateway-bm"))

	cluster.Status.Versions.Fabricator.NodeConfig = "v1.2.3-dirty-Ak2124"
	require.ErrorContains(t, c.checkClusterVersion(cluster, "gateway-bm"), "Ak2124")
}

func TestCheckFreshInstall(t *testing.T) {
	resetAt := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	now := resetAt.Add(30 * time.Minute)

	for _, tt := range []struct {
		name string
		out  string
		err  bool
	}{
		{name: "fresh", out: fmt.Sprintf("%d\n%d\n", now.Add(-5*time.Minute).Unix(), now.Unix())},
		{name: "fresh-node-clock-behind", out: fmt.Sprintf("%d %d", now.Add(-3*time.Hour-5*time.Minute).Unix(), now.Add(-3*time.Hour).Unix())},
		{name: "fresh-node-clock-ahead", out: fmt.Sprintf("%d %d", now.Add(3*time.Hour-5*time.Minute).Unix(), now.Add(3*time.Hour).Unix())},
		{name: "within-slack", out: fmt.Sprintf("%d %d", resetAt.Add(-time.Minute).Unix(), now.Unix())},
		{name: "old-os", out: fmt.Sprintf("%d %d", resetAt.Add(-24*time.Hour).Unix(), now.Unix()), err: true},
		{name: "before-slack", out: fmt.Sprintf("%d %d", resetAt.Add(-3*time.Minute).Unix(), now.Unix()), err: true},
		{name: "bad-output", out: "stat: cannot statx", err: true},
		{name: "not-a-number", out: "abc 123", err: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFreshInstall(tt.out, now, resetAt)
			if tt.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
