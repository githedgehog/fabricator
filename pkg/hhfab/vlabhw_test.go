// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	vlabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/vlab"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestResolveHWModes(t *testing.T) {
	for _, tt := range []struct {
		name     string
		flags    HWModeFlags
		upgrade  bool
		switches HWMode
		nodes    HWMode
		err      bool
	}{
		{name: "default", switches: HWModeAuto, nodes: HWModeAuto},
		{name: "separate", flags: HWModeFlags{Switches: HWModeDisabled}, switches: HWModeDisabled, nodes: HWModeAuto},
		{name: "all", flags: HWModeFlags{All: HWModeReinstall}, switches: HWModeReinstall, nodes: HWModeReinstall},
		{name: "all-and-separate", flags: HWModeFlags{All: HWModeAuto, Nodes: HWModeAuto}, err: true},
		{name: "invalid", flags: HWModeFlags{Nodes: "force"}, err: true},
		{name: "upgrade", upgrade: true, switches: HWModeAuto, nodes: HWModeAuto},
		{name: "upgrade-reinstall", flags: HWModeFlags{Switches: HWModeReinstall}, upgrade: true, err: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			switches, nodes, err := resolveHWModes(tt.flags, tt.upgrade)
			if tt.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.switches, switches)
			require.Equal(t, tt.nodes, nodes)
		})
	}
}

func TestValidateOnReady(t *testing.T) {
	require.NoError(t, ValidateOnReady(nil))
	require.NoError(t, ValidateOnReady([]OnReady{OnReadyInspect, FromShortOnReady("vpcs")}))
	require.ErrorContains(t, ValidateOnReady([]OnReady{"unknown"}), "unsupported")

	for _, cmd := range []string{"switch-reinstall", "reinstall"} {
		err := ValidateOnReady([]OnReady{OnReadyInspect, FromShortOnReady(cmd)})
		require.ErrorContains(t, err, "default", cmd)
		require.ErrorContains(t, err, "--hw-switches", cmd)
	}
}

func TestIsUnused(t *testing.T) {
	for _, tt := range []struct {
		name        string
		annotations map[string]string
		description string
		unused      bool
		err         bool
	}{
		{name: "none"},
		{name: "annotation", annotations: map[string]string{vlabcomp.UnusedAnnotation: "true"}, unused: true},
		{name: "description", description: vlabcomp.UnusedDescription, unused: true},
		{name: "annotation-wins", annotations: map[string]string{vlabcomp.UnusedAnnotation: "false"}, description: vlabcomp.UnusedDescription},
		{name: "other-description", description: "unused spine"},
		{name: "invalid", annotations: map[string]string{vlabcomp.UnusedAnnotation: "yes"}, err: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unused, err := vlabcomp.IsUnused(tt.annotations, tt.description)
			if tt.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.unused, unused)
		})
	}
}

func hwSwitch(name string, annotations map[string]string, description string) *wiringapi.Switch {
	sw := &wiringapi.Switch{Name: name, Namespace: kmetav1.NamespaceDefault, Annotations: map[string]string{
		HHFabCfgType:                   HHFabCfgTypeHW,
		hhfctl.HHFabCfgPower + "/psu1": "http://192.0.2.10/outlet/1",
		hhfctl.HHFabCfgSerial:          hhfctl.HHFabCfgSerialSchemeSSH + "192.0.2.11:22",
	}}
	maps.Copy(sw.Annotations, annotations)
	sw.Spec.Description = description

	return sw
}

func hwNode(name string, annotations map[string]string) fabapi.FabNode {
	node := fabapi.FabNode{Name: name, Namespace: comp.FabNamespace, Annotations: map[string]string{HHFabCfgType: HHFabCfgTypeHW}}
	maps.Copy(node.Annotations, annotations)

	return node
}

func switchNames(sws []wiringapi.Switch) []string {
	names := make([]string, 0, len(sws))
	for _, sw := range sws {
		names = append(names, sw.Name)
	}

	return names
}

func TestDropUnusedHW(t *testing.T) {
	ctx := t.Context()
	unused := map[string]string{vlabcomp.UnusedAnnotation: "true"}

	newConfig := func(t *testing.T, objs ...kclient.Object) *Config {
		t.Helper()

		l := apiutil.NewLoader()
		require.NoError(t, l.Add(ctx, objs...))

		c := &Config{Client: l.GetClient()}
		for _, obj := range objs {
			if node, ok := obj.(*fabapi.FabNode); ok {
				c.Nodes = append(c.Nodes, *node)
			}
		}

		return c
	}

	used := map[string]string{vlabcomp.UnusedAnnotation: "false"}
	bmNode := hwNode("gateway-bm", map[string]string{vlabcomp.UnusedAnnotation: "true", HHFabCfgBMC: "https://192.0.2.20"})
	usedNode := hwNode("gateway-1", used)
	c := newConfig(t,
		hwSwitch("leaf-1", nil, ""),
		hwSwitch("spine-1", unused, ""),
		hwSwitch("spine-2", nil, vlabcomp.UnusedDescription),
		hwSwitch("spine-3", used, vlabcomp.UnusedDescription),
		&bmNode,
		&usedNode,
	)
	require.NoError(t, c.dropUnusedHW(ctx))

	switches := &wiringapi.SwitchList{}
	require.NoError(t, c.Client.List(ctx, switches))
	require.ElementsMatch(t, []string{"leaf-1", "spine-3"}, switchNames(switches.Items))
	require.ElementsMatch(t, []string{"spine-1", "spine-2"}, switchNames(c.UnusedHW.Switches))
	spine3 := &wiringapi.Switch{}
	require.NoError(t, c.Client.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: "spine-3"}, spine3))
	require.NotContains(t, spine3.Annotations, vlabcomp.UnusedAnnotation)
	require.Len(t, c.Nodes, 1)
	require.NotContains(t, c.Nodes[0].Annotations, vlabcomp.UnusedAnnotation)
	require.Len(t, c.UnusedHW.Nodes, 1)
	require.Error(t, c.Client.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: "gateway-bm"}, &fabapi.FabNode{}))

	// unused ones are available for power by name only
	nodes, err := c.selectBMCNodes([]string{"gateway-bm"}, true)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"gateway-bm": "https://192.0.2.20"}, nodes)
	_, err = c.selectBMCNodes([]string{"gateway-bm"}, false)
	require.Error(t, err)

	virtual := &wiringapi.Switch{Name: "leaf-2", Namespace: kmetav1.NamespaceDefault, Annotations: unused}
	require.ErrorContains(t, newConfig(t, virtual).dropUnusedHW(ctx), "only hardware")

	// not VLAB hardware, so the description is only a description
	described := &wiringapi.Switch{Name: "leaf-2", Namespace: kmetav1.NamespaceDefault}
	described.Spec.Description = vlabcomp.UnusedDescription
	c = newConfig(t, described)
	require.NoError(t, c.dropUnusedHW(ctx))
	require.Empty(t, c.UnusedHW.Switches)

	conn := &wiringapi.Connection{Name: "server-1--unbundled--spine-1", Namespace: kmetav1.NamespaceDefault}
	conn.Spec.Unbundled = &wiringapi.ConnUnbundled{Link: wiringapi.ServerToSwitchLink{
		Server: wiringapi.NewBasePortName("server-1/enp2s1"),
		Switch: wiringapi.NewBasePortName("spine-1/E1/1"),
	}}
	require.ErrorContains(t, newConfig(t, hwSwitch("spine-1", unused, ""), conn).dropUnusedHW(ctx), "uses it")

	gwNode := hwNode("gateway-bm", unused)
	gw := &gwapi.Gateway{Name: "gateway-bm", Namespace: kmetav1.NamespaceDefault}
	require.ErrorContains(t, newConfig(t, &gwNode, gw).dropUnusedHW(ctx), "uses it")
}

func TestPlanHW(t *testing.T) {
	ctx := t.Context()

	l := apiutil.NewLoader()
	require.NoError(t, l.Add(ctx,
		hwSwitch("leaf-1", nil, ""),
		hwSwitch("leaf-2", nil, ""),
		&wiringapi.Switch{Name: "leaf-3", Namespace: kmetav1.NamespaceDefault},
	))
	c := &Config{
		WorkDir: t.TempDir(),
		Client:  l.GetClient(),
		Nodes: []fabapi.FabNode{
			hwNode("gateway-bm", map[string]string{HHFabCfgBMC: "https://127.0.0.1"}),
			hwNode("gateway-hw", nil),
			{Name: "gateway-1"},
		},
	}
	require.NoError(t, c.setHWInstalled("leaf-2"))

	opts := VLABRunOpts{
		HWSwitches:  HWModeAuto,
		HWNodes:     HWModeAuto,
		PDUUsername: "admin",
		PDUPassword: "password",
		BMC:         BMCOpts{Username: "admin", Password: "password"},
	}

	t.Run("auto", func(t *testing.T) {
		plan, err := c.planHW(ctx, opts)
		if err != nil {
			require.ErrorContains(t, err, "expect") // not installed locally

			t.Skip("no expect")
		}
		require.Equal(t, []string{"leaf-1"}, plan.reinstallSwitches)
		require.Equal(t, []string{"leaf-2"}, switchNames(plan.powerOnSwitches))
		require.Equal(t, map[string]string{"gateway-bm": "https://127.0.0.1"}, plan.reinstallNodes)
		require.Equal(t, map[string]bool{"gateway-hw": true}, plan.manualNodes)
	})

	t.Run("auto-installed", func(t *testing.T) {
		require.NoError(t, c.setHWInstalled("leaf-1"))
		require.NoError(t, c.setHWInstalled("gateway-bm"))
		defer func() {
			require.NoError(t, c.resetHWInstalled("leaf-1"))
			require.NoError(t, c.resetHWInstalled("gateway-bm"))
		}()

		plan, err := c.planHW(ctx, opts)
		require.NoError(t, err)
		require.Empty(t, plan.reinstallSwitches)
		require.ElementsMatch(t, []string{"leaf-1", "leaf-2"}, switchNames(plan.powerOnSwitches))
		require.Empty(t, plan.reinstallNodes)
		require.Equal(t, map[string]string{"gateway-bm": "https://127.0.0.1"}, plan.powerOnNodes)

		// powering on needs the credentials too
		noCreds := opts
		noCreds.PDUUsername = ""
		_, err = c.planHW(ctx, noCreds)
		require.ErrorIs(t, err, ErrPDUCredentials)
		noCreds = opts
		noCreds.BMC = BMCOpts{}
		_, err = c.planHW(ctx, noCreds)
		require.ErrorIs(t, err, ErrBMCCredentials)
	})

	t.Run("unused", func(t *testing.T) {
		c.UnusedHW = UnusedHW{
			Switches: []wiringapi.Switch{*hwSwitch("spine-1", nil, "")},
			Nodes:    []fabapi.FabNode{hwNode("gateway-off", map[string]string{HHFabCfgBMC: "https://127.0.0.2"})},
		}
		defer func() { c.UnusedHW = UnusedHW{} }()

		disabled := opts
		disabled.HWSwitches, disabled.HWNodes = HWModeDisabled, HWModeDisabled
		disabled.HWPowerOffUnused = true
		disabled.PDUPassword = ""
		_, err := c.planHW(ctx, disabled)
		require.ErrorIs(t, err, ErrPDUCredentials)

		disabled = opts
		disabled.HWSwitches, disabled.HWNodes = HWModeDisabled, HWModeDisabled
		disabled.HWPowerOffUnused = true
		disabled.BMC = BMCOpts{}
		_, err = c.planHW(ctx, disabled)
		require.ErrorIs(t, err, ErrBMCCredentials)

		// not powered off, so no credentials needed
		disabled.PDUUsername, disabled.HWPowerOffUnused = "", false
		_, err = c.planHW(ctx, disabled)
		require.NoError(t, err)
	})

	t.Run("no-credentials", func(t *testing.T) {
		noCreds := opts
		noCreds.HWNodes = HWModeDisabled
		noCreds.PDUPassword = ""
		_, err := c.planHW(ctx, noCreds)
		require.ErrorIs(t, err, ErrPDUCredentials)

		noCreds = opts
		noCreds.HWSwitches = HWModeDisabled
		noCreds.BMC.Password = ""
		_, err = c.planHW(ctx, noCreds)
		require.ErrorIs(t, err, ErrBMCCredentials)
	})

	t.Run("disabled", func(t *testing.T) {
		disabled := opts
		disabled.HWSwitches, disabled.HWNodes = HWModeDisabled, HWModeDisabled
		plan, err := c.planHW(ctx, disabled)
		require.NoError(t, err)
		require.Empty(t, plan.reinstallSwitches)
		require.Empty(t, plan.powerOnSwitches)
		require.Empty(t, plan.reinstallNodes)
		require.Equal(t, map[string]bool{"gateway-bm": true, "gateway-hw": true}, plan.manualNodes)
	})

	t.Run("reinstall-no-bmc", func(t *testing.T) {
		reinstall := opts
		reinstall.HWSwitches, reinstall.HWNodes = HWModeDisabled, HWModeReinstall
		_, err := c.planHW(ctx, reinstall)
		require.ErrorContains(t, err, "gateway-hw")
	})

	t.Run("no-serial", func(t *testing.T) {
		sw := hwSwitch("leaf-4", nil, "")
		delete(sw.Annotations, hhfctl.HHFabCfgSerial)
		l := apiutil.NewLoader()
		require.NoError(t, l.Add(ctx, sw))
		c := &Config{WorkDir: t.TempDir(), Client: l.GetClient()}

		reinstall := opts
		reinstall.HWSwitches = HWModeReinstall
		_, err := c.planHW(ctx, reinstall)
		require.ErrorContains(t, err, "serial console")

		// installed manually in auto
		plan, err := c.planHW(ctx, opts)
		require.NoError(t, err)
		require.Empty(t, plan.reinstallSwitches)
		require.Empty(t, plan.powerOnSwitches)
	})

	t.Run("upgrade", func(t *testing.T) {
		upgrade := opts
		upgrade.AutoUpgrade = true
		plan, err := c.planHW(ctx, upgrade)
		require.NoError(t, err)
		require.Empty(t, plan.reinstallSwitches)
		// installed or not, as a VLAB from an older version has no markers
		require.ElementsMatch(t, []string{"leaf-1", "leaf-2"}, switchNames(plan.powerOnSwitches))
		require.Empty(t, plan.reinstallNodes)
		require.ElementsMatch(t, []string{"gateway-bm", "gateway-hw"}, plan.upgradeNodes)
		require.Equal(t, map[string]string{"gateway-bm": "https://127.0.0.1"}, plan.powerOnNodes)
	})
}
