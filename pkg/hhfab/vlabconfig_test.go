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
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
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

func TestCreateVLABConfigInterconnectLinks(t *testing.T) {
	l := apiutil.NewLoader()
	b := &VLABBuilderDefault{
		ExtBGPCount:          1,
		ExtOrphanConnCount:   1,
		ExtraFabric:          true,
		InterconnectFabrics:  true,
		InterconnectLinks:    2,
		DefaultSwitchProfile: meta.SwitchProfileVS,
	}
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Default()
	require.NoError(t, b.Build(t.Context(), l, f, nil))

	cfg, err := createVLABConfig(t.Context(), nil, nil, l.GetClient())
	require.NoError(t, err)

	// each pair is on its own port of the same two leaves and is cabled to its own peer
	for _, leaf := range []string{"leaf-03", "leaf-04"} {
		ports := make([]string, 0, 2)
		for _, suffix := range []string{"--interconnect", "--interconnect-2"} {
			conn := &wiringapi.Connection{}
			require.NoError(t, l.GetClient().Get(t.Context(), kclient.ObjectKey{Name: leaf + suffix}, conn))
			ports = append(ports, conn.Spec.External.Link.Switch.Port)
		}
		require.NotEqual(t, ports[0], ports[1])
		for _, port := range ports {
			require.Contains(t, cfg.VMs[leaf].NICs, strings.SplitN(port, "/", 2)[1])
		}
	}

	for _, name := range []string{"to-fabric-b", "to-fb-2", "to-default", "to-def-2"} {
		ext := &vpcapi.External{}
		require.NoError(t, l.GetClient().Get(t.Context(), kclient.ObjectKey{Name: name}, ext))
		require.NotEmpty(t, ext.Annotations[VLABInterconnectAnnotation])
	}
	ext := &vpcapi.External{}
	require.NoError(t, l.GetClient().Get(t.Context(), kclient.ObjectKey{Name: "to-fb-2"}, ext))
	require.Equal(t, "to-def-2", ext.Annotations[VLABInterconnectAnnotation])
	require.Equal(t, uint32(64912), ext.Spec.LocalASN)

	require.Len(t, cfg.Externals.NICs, 1)
}

func TestInterconnectLinksNeedInterconnect(t *testing.T) {
	b := &VLABBuilderDefault{
		ExtBGPCount:          1,
		ExtraFabric:          true,
		InterconnectLinks:    2,
		DefaultSwitchProfile: meta.SwitchProfileVS,
	}
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Default()
	require.ErrorContains(t, b.Build(t.Context(), apiutil.NewLoader(), f, nil), "interconnecting fabrics")
}

func TestInterconnectPairsMaxLinks(t *testing.T) {
	pairs := (&VLABBuilderDefault{InterconnectLinks: 255}).interconnectPairs()
	require.Len(t, pairs, 255)
	require.Equal(t, "to-fb-255", pairs[254].MainExt)
	require.Equal(t, "100.64.254.1/30", pairs[254].MainIP)
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

				port := nicID - 1
				require.Contains(t, vmNICs[nicID], fmt.Sprintf("-device pcie-root-port,id=%s%d,bus=pcie.0,chassis=%d,addr=0x%x,hotplug=off ",
					VLABRootPortPrefix, port, port+1, vlabcomp.RootPortBaseSlot+port), name)
				require.Contains(t, vmNICs[nicID], "-device igb,", name)
				require.Contains(t, vmNICs[nicID], fmt.Sprintf(",bus=%s%d", VLABRootPortPrefix, port), name)
			}
		})
	}
}

func TestVLABRootPortBaseBus(t *testing.T) {
	// the firmware numbers the DMI-to-PCI and PCI bridge buses first as they're on the lower slots
	require.Equal(t, 2*VLABPCIBridges+1, vlabcomp.RootPortBaseBus)
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

func TestMarkStagedAddFabric(t *testing.T) {
	l := apiutil.NewLoader()
	b := &VLABBuilderDefault{
		ExtBGPCount:          1,
		ExtOrphanConnCount:   1,
		ExtraFabric:          true,
		AddFabric:            true,
		InterconnectFabrics:  true,
		DefaultSwitchProfile: meta.SwitchProfileVS,
	}
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Default()
	require.NoError(t, b.Build(t.Context(), l, f, nil))
	require.NoError(t, b.MarkStaged(t.Context(), l))

	objs, err := apiutil.ListWiring(t.Context(), l.GetClient())
	require.NoError(t, err)

	staged := map[string]bool{}
	installed := map[string]bool{}
	for _, obj := range objs {
		key := fmt.Sprintf("%T/%s", obj, obj.GetName())
		if apiutil.IsStaged(obj) {
			staged[key] = true
		} else {
			installed[key] = true
		}
	}

	// the extra fabric with its switches and its interconnect, on both sides
	for _, key := range []string{
		"*v1beta1.Fabric/fabric-b", "*v1beta1.IPv4Namespace/fabric-b",
		"*v1beta1.Switch/leaf-04", "*v1beta1.Switch/leaf-05",
		"*v1beta1.Connection/leaf-03--interconnect", "*v1beta1.Connection/leaf-04--interconnect",
		"*v1beta1.External/to-fabric-b", "*v1beta1.External/to-default",
		"*v1beta1.ExternalAttachment/leaf-03--to-fabric-b", "*v1beta1.ExternalAttachment/leaf-04--to-default",
	} {
		require.True(t, staged[key], "%s must be staged", key)
	}

	// everything of the default fabric, including its virtual external, is installed
	for _, key := range []string{
		"*v1beta1.Fabric/default", "*v1beta1.Switch/leaf-01", "*v1beta1.Switch/leaf-02", "*v1beta1.Switch/leaf-03",
		"*v1beta1.External/ext-bgp-01",
	} {
		require.True(t, installed[key], "%s must be installed", key)
	}

	// a server is staged exactly when all of its connections are
	stagedConns := map[string]bool{}
	for _, obj := range objs {
		if conn, ok := obj.(*wiringapi.Connection); ok {
			stagedConns[conn.Name] = apiutil.IsStaged(conn)
		}
	}
	for _, obj := range objs {
		server, ok := obj.(*wiringapi.Server)
		if !ok {
			continue
		}

		all, found := true, false
		for _, obj := range objs {
			conn, ok := obj.(*wiringapi.Connection)
			if !ok {
				continue
			}
			_, servers, _, _, err := conn.Spec.Endpoints()
			require.NoError(t, err)
			if slices.Contains(servers, server.Name) {
				found = true
				all = all && stagedConns[conn.Name]
			}
		}
		require.True(t, found, "server %s has no connection", server.Name)
		require.Equal(t, all, apiutil.IsStaged(server), "server %s", server.Name)
	}

	// without AddFabric nothing is staged
	l2 := apiutil.NewLoader()
	b.AddFabric = false
	require.NoError(t, b.Build(t.Context(), l2, f, nil))
	require.NoError(t, b.MarkStaged(t.Context(), l2))
	none, err := apiutil.ListStaged(t.Context(), l2.GetClient())
	require.NoError(t, err)
	require.Empty(t, none)
}
