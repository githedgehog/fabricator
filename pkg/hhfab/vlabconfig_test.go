// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	vlabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/vlab"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGetNICID(t *testing.T) {
	for _, tt := range []struct {
		nic  string
		want uint
		err  bool
	}{
		{
			nic: "eth0",
			err: true,
		},
		{
			nic: "eno0",
			err: true,
		},
		{
			nic: "enp1s1",
			err: true,
		},
		{
			nic: "x1",
			err: true,
		},
		{
			nic: "M2",
			err: true,
		},
		{
			nic: "E1",
			err: true,
		},
		{
			nic: "E1/",
			err: true,
		},
		{
			nic: "E1/1/",
			err: true,
		},
		{
			nic: "E1/1/1",
			err: true,
		},
		{
			nic: "E2",
			err: true,
		},
		{
			nic: "E2/",
			err: true,
		},
		{
			nic: "E2/1/",
			err: true,
		},
		{
			nic: "E2/1/1",
			err: true,
		},
		{
			nic: "Management0",
			err: true,
		},
		{
			nic: "Management1",
			err: true,
		},
		{
			nic:  "M1",
			want: 0,
		},
		{
			nic:  "E1/1",
			want: 1,
		},
		{
			nic:  "E1/2",
			want: 2,
		},
		{
			nic:  "E1/99",
			want: 99,
		},
		{
			nic:  "enp2s0",
			want: 0,
		},
		{
			nic:  "enp2s1",
			want: 1,
		},
		{
			nic:  "enp2s2",
			want: 2,
		},
		{
			nic:  "enp2s99",
			want: 99,
		},
		{
			nic:  "enp2s0np1",
			want: 0,
		},
		{
			nic:  "enp2s0np2",
			want: 0,
		},
		{
			nic:  "enp2s0np3",
			want: 0,
		},
		{
			nic:  "enp2s99np42",
			want: 99,
		},
		{
			nic: "enp2s99np42np1",
			err: true,
		},
	} {
		t.Run(tt.nic, func(t *testing.T) {
			got, err := getNICID(tt.nic)

			if tt.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCreateVLABConfigInterconnect(t *testing.T) {
	l := apiutil.NewLoader()
	b := &VLABBuilderDefault{
		ExtBGPCount:          1,
		ExtOrphanConnCount:   1,
		ExtraFabric:          true,
		InterconnectFabrics:  true,
		DefaultSwitchProfile: meta.SwitchProfileVS,
	}
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Default()
	require.NoError(t, b.Build(t.Context(), l, f, nil))

	cfg, err := createVLABConfig(t.Context(), nil, nil, l.GetClient())
	require.NoError(t, err)

	// leaf-03 is the last leaf of the main fabric, and also has the connection to the virtual external
	ports := []string{} //nolint:prealloc
	for _, name := range []string{"leaf-03--interconnect", "leaf-04--interconnect"} {
		conn := &wiringapi.Connection{}
		require.NoError(t, l.GetClient().Get(t.Context(), kclient.ObjectKey{Name: name}, conn))
		ports = append(ports, conn.Spec.External.Link.Switch.Port)
	}
	require.Equal(t, NICTypeDirect+NICTypeSep+ports[1], cfg.VMs["leaf-03"].NICs[strings.SplitN(ports[0], "/", 2)[1]])
	require.Equal(t, NICTypeDirect+NICTypeSep+ports[0], cfg.VMs["leaf-04"].NICs[strings.SplitN(ports[1], "/", 2)[1]])

	require.Equal(t, []string{"ext-bgp-01"}, slices.Collect(maps.Keys(cfg.Externals.VRFs)))
	require.Len(t, cfg.Externals.NICs, 1)
}

func TestVLABSSHKey(t *testing.T) {
	workDir := t.TempDir()

	pub, err := getVLABSSHKey(workDir)
	require.NoError(t, err)
	require.Empty(t, pub, "no key without VLAB dir")

	require.NoError(t, os.MkdirAll(filepath.Join(workDir, VLABDir), 0o700))
	pub, err = getVLABSSHKey(workDir)
	require.NoError(t, err)
	require.Empty(t, pub, "no key without key file")
	require.NoFileExists(t, filepath.Join(workDir, VLABDir, VLABSSHKeyFile), "key must never be created")

	created, _, err := getOrCreateSSHKey(filepath.Join(workDir, VLABDir, VLABSSHKeyFile))
	require.NoError(t, err)

	pub, err = getVLABSSHKey(workDir)
	require.NoError(t, err)
	require.Equal(t, created, pub, "must match the keygen format vlab up uses, otherwise the key is added twice")

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	c.Fab.Spec.Config.Control.DefaultUser.AuthorizedKeys = []string{"ssh-ed25519 existing"}
	c.Fab.Spec.Config.Fabric.DefaultSwitchUsers = map[string]fabapi.SwitchUser{"admin": {Role: "admin"}}

	require.True(t, c.addAuthorizedKey(pub))
	require.False(t, c.addAuthorizedKey(pub), "already added")
	require.Equal(t, []string{"ssh-ed25519 existing", pub}, c.Fab.Spec.Config.Control.DefaultUser.AuthorizedKeys)
	require.Equal(t, []string{pub}, c.Fab.Spec.Config.Fabric.DefaultSwitchUsers["admin"].AuthorizedKeys)
}

func TestCreateVLABConfigHardwareGateway(t *testing.T) {
	nodes := []fabapi.FabNode{} //nolint:prealloc
	for _, name := range []string{"gateway-1", "gateway-2"} {
		nodes = append(nodes, fabapi.FabNode{
			Name: name,
			Spec: fabapi.FabNodeSpec{
				Roles:      []fabapi.FabNodeRole{fabapi.NodeRoleGateway},
				Management: fabapi.ControlNodeManagement{Interface: "enp2s0"},
			},
		})
	}

	l := apiutil.NewLoader()
	b := &VLABBuilderDefault{
		GatewayLogLevel:      "info",
		DefaultSwitchProfile: meta.SwitchProfileVS,
	}
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Default()
	require.NoError(t, b.Build(t.Context(), l, f, nodes))

	// switch port -> gateway port for the hardware gateway
	hwLinks := map[string]string{}
	conns := &wiringapi.ConnectionList{}
	require.NoError(t, l.GetClient().List(t.Context(), conns))
	for _, conn := range conns.Items {
		if conn.Spec.Gateway == nil {
			continue
		}
		for _, link := range conn.Spec.Gateway.Links {
			if strings.HasPrefix(link.Gateway.Port, "gateway-2/") {
				hwLinks[link.Switch.Port] = link.Gateway.Port
			}
		}
	}
	require.NotEmpty(t, hwLinks)

	nodes[1].Annotations = map[string]string{HHFabCfgType: HHFabCfgTypeHW}

	_, err := createVLABConfig(t.Context(), nil, nodes, l.GetClient())
	require.ErrorContains(t, err, "missing passthrough link for gateway-2/")

	pcis := map[string]string{}
	for swPort, gwPort := range hwLinks {
		pci := fmt.Sprintf("0000:3b:00.%d", len(pcis))
		pcis[swPort] = pci
		nodes[1].Annotations[HHFabCfgLinkPrefix+strings.SplitN(gwPort, "/", 2)[1]] = HHFabCfgPCIPrefix + pci
	}

	cfg, err := createVLABConfig(t.Context(), nil, nodes, l.GetClient())
	require.NoError(t, err)
	require.Contains(t, cfg.VMs, "gateway-1")
	require.NotContains(t, cfg.VMs, "gateway-2")
	for swPort, pci := range pcis {
		parts := strings.SplitN(swPort, "/", 2)
		require.Equal(t, NICTypePassthrough+NICTypeSep+pci, cfg.VMs[parts[0]].NICs[parts[1]], swPort)
	}
}

func TestVLABGatewayNICs(t *testing.T) {
	nodes := []fabapi.FabNode{{
		Name: "gateway-1",
		Spec: fabapi.FabNodeSpec{
			Roles:      []fabapi.FabNodeRole{fabapi.NodeRoleGateway},
			Management: fabapi.ControlNodeManagement{Interface: vlabcomp.GatewayKernel(0)},
		},
	}}

	for _, driver := range GatewayDrivers {
		t.Run(driver, func(t *testing.T) {
			l := apiutil.NewLoader()
			b := &VLABBuilderDefault{
				GatewayLogLevel:      "info",
				GatewayDriver:        driver,
				DefaultSwitchProfile: meta.SwitchProfileVS,
			}
			f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
			f.Default()
			require.NoError(t, b.Build(t.Context(), l, f, nodes))

			gw := &gwapi.Gateway{}
			require.NoError(t, l.GetClient().Get(t.Context(), kclient.ObjectKey{Name: "gateway-1", Namespace: kmetav1.NamespaceDefault}, gw))
			require.Equal(t, gwapi.GatewayDriver(driver), gw.Spec.Driver)
			require.NotEmpty(t, gw.Spec.Interfaces)

			cfg, err := createVLABConfig(t.Context(), nil, nodes, l.GetClient())
			require.NoError(t, err)
			vlab, err := vlabFromConfig(cfg, VLABRunOpts{})
			require.NoError(t, err)

			idx := slices.IndexFunc(vlab.VMs, func(vm VM) bool { return vm.Name == "gateway-1" })
			require.GreaterOrEqual(t, idx, 0)
			vmNICs := vlab.VMs[idx].NICs
			require.Contains(t, vmNICs[0], "-device e1000,")
			require.Contains(t, vmNICs[0], ",bus="+VLABPCIBridgePrefix+"0,addr=0x0")

			for name, iface := range gw.Spec.Interfaces {
				nicID, err := getNICID(name)
				require.NoError(t, err, name)
				require.Equal(t, vlabcomp.GatewayPCI(uint8(nicID)), iface.PCI, name) //nolint:gosec

				require.Equal(t, vlabcomp.GatewayKernel(uint8(nicID)), iface.Kernel, name) //nolint:gosec

				var slot uint
				_, err = fmt.Sscanf(iface.PCI, "0000:00:%02x.0", &slot)
				require.NoError(t, err, iface.PCI)
				require.Contains(t, vmNICs[nicID], "-device igb,", name)
				require.Contains(t, vmNICs[nicID], fmt.Sprintf(",bus=pcie.0,addr=0x%x", slot), name)
			}
		})
	}
}

func TestResolveVMSizes(t *testing.T) {
	saved := DefaultSizes
	saved.Gateway = VMSize{CPU: 4, RAM: 6144, Disk: 100}
	saved.Server = VMSize{}

	sizes, err := resolveVMSizes(saved, VMSizes{Control: VMSize{RAM: 4096}})
	require.NoError(t, err)
	require.Equal(t, VMSize{CPU: DefaultSizes.Gateway.CPU, RAM: DefaultSizes.Gateway.RAM, Disk: 100}, sizes.Gateway)
	require.Equal(t, DefaultSizes.Server, sizes.Server)
	require.Equal(t, VMSize{CPU: DefaultSizes.Control.CPU, RAM: 4096, Disk: DefaultSizes.Control.Disk}, sizes.Control)
	require.Equal(t, DefaultSizes.Switch, sizes.Switch)
}
