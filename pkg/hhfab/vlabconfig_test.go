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
	kyaml "sigs.k8s.io/yaml"
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

	require.NoError(t, l.GetClient().Create(t.Context(), &vpcapi.IPv4Namespace{
		Name:      "ipns-other-fabric",
		Namespace: kmetav1.NamespaceDefault,
		Spec: vpcapi.IPv4NamespaceSpec{
			Topology: vpcapi.IPv4NamespaceTopology{Fabric: "other"},
			Subnets:  []string{"10.62.0.0/16"},
		},
	}))

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
				GatewayWorkers:       3,
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

			// a gateway VM without its queues, e.g. from a VLAB config without them, can't run
			_, err = vlabFromConfig(cfg, VLABRunOpts{})
			require.ErrorContains(t, err, "no NIC queues for gateway VM")

			require.NoError(t, setGatewayQueues(t.Context(), cfg, l.GetClient()))
			queues := gatewayNICQueues(3)
			require.Equal(t, queues, cfg.VMs["gateway-1"].Queues)

			// the queues are never saved, so the VLAB configs of other releases keep working
			data, err := kyaml.Marshal(cfg)
			require.NoError(t, err)
			require.NotContains(t, string(data), "queues")

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
				require.Contains(t, vmNICs[nicID], "-device virtio-net-pci,", name)
				require.Contains(t, vmNICs[nicID], fmt.Sprintf(",mq=on,vectors=%d,speed=%s,duplex=full", 2*int(queues)+2, VLABVirtioNetSpeed), name)
				require.Contains(t, vmNICs[nicID], ",disable-legacy=on,iommu_platform=on,", name)
				require.Contains(t, vmNICs[nicID], fmt.Sprintf(",bus=%s%d", VLABRootPortPrefix, port), name)

				// the gateway end of the link is its multi-queue tap, the switch end a plain one under e1000
				_, tap, ok := strings.Cut(vmNICs[nicID], "-netdev tap,ifname=")
				require.True(t, ok, name)
				tap, _, _ = strings.Cut(tap, ",")
				require.Contains(t, vmNICs[nicID], fmt.Sprintf(",vhost=off,queues=%d,", queues), name)

				netIdx := slices.IndexFunc(vlab.Nets, func(n VLABNet) bool { return strings.HasPrefix(tap, n.Name+"p") })
				require.GreaterOrEqual(t, netIdx, 0, tap)
				n := vlab.Nets[netIdx]
				require.Equal(t, VLABNetKindLink, n.Kind, tap)
				tapIdx := slices.IndexFunc([]string{n.TapName(0), n.TapName(1)}, func(name string) bool { return name == tap })
				require.GreaterOrEqual(t, tapIdx, 0, tap)
				require.Equal(t, map[int]int{tapIdx: int(queues)}, n.Queues, tap)

				other := "-netdev tap,ifname=" + n.TapName(1-tapIdx) + ",script=no,downscript=no,id="
				found := false
				for _, vm := range vlab.VMs {
					for _, nic := range vm.NICs {
						if strings.Contains(nic, other) {
							require.Equal(t, VMTypeSwitch, vm.Type, nic)
							require.Contains(t, nic, "-device e1000,", nic)
							found = true
						}
					}
				}
				require.True(t, found, "no switch end for %s", tap)
			}
		})
	}
}

func TestVLABRootPortBaseBus(t *testing.T) {
	// the firmware numbers the DMI-to-PCI and PCI bridge buses first as they're on the lower slots
	require.Equal(t, 2*VLABPCIBridges+1, vlabcomp.RootPortBaseBus)
}

func TestVLABNetSpec(t *testing.T) {
	for _, tt := range []struct {
		spec string
		net  VLABNet
	}{
		{"hhmgmt=mgmt,taps=0", VLABNet{Name: VLABMgmt, Kind: VLABNetKindMgmt}},
		{"hhmgmt=mgmt,taps=12,iface=enp2s1,iface=enp2s2", VLABNet{Name: VLABMgmt, Kind: VLABNetKindMgmt, Taps: 12, Ifaces: []string{"enp2s1", "enp2s2"}}},
		{"hhlink0=link", VLABNet{Name: "hhlink0", Kind: VLABNetKindLink, Taps: 2}},
		{"hhlink17=link,p1.queues=8", VLABNet{Name: "hhlink17", Kind: VLABNetKindLink, Taps: 2, Queues: map[int]int{1: 8}}},
		{"hhlink3=link,p0.queues=4,p1.queues=8", VLABNet{Name: "hhlink3", Kind: VLABNetKindLink, Taps: 2, Queues: map[int]int{0: 4, 1: 8}}},
	} {
		t.Run(tt.spec, func(t *testing.T) {
			n, err := ParseVLABNet(tt.spec)
			require.NoError(t, err)
			require.Equal(t, tt.net, n)
			require.Equal(t, tt.spec, n.String())
		})
	}

	for _, spec := range []string{
		"",
		"hhmgmt",
		"hhmgmt=bridge",
		"hhother=mgmt,taps=1",
		"hhmgmt=mgmt,taps=101",
		"hhmgmt=mgmt,taps=-1",
		"hhmgmt=mgmt,mtu=1500",
		"hhmgmt=mgmt,taps",
		"hhmgmt=mgmt,taps=",
		"hhlink=link",
		"hhlinkx=link",
		"hhlink0=link,taps=2",
		"hhlink0=link,iface=eth0",
		"hhlink0=link,p2.queues=8",
		"hhlink0=link,p1.queues=1",
		"hhlink0=link,p1.queues=x",
		"hhlink0=link,px.queues=8",
	} {
		t.Run("invalid "+spec, func(t *testing.T) {
			_, err := ParseVLABNet(spec)
			require.Error(t, err)
		})
	}

	n := VLABNet{Name: "hhlink5", Kind: VLABNetKindLink, Taps: 2}
	require.Equal(t, "hhlink5p0", n.TapName(0))
	require.Equal(t, "hhlink5p1", n.TapName(1))
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

func TestExternalIgnitionStaticReturnRoutes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		subnets []string
		want    []string
	}{
		{name: "namespace subnets", subnets: []string{"10.60.0.0/16", "10.61.0.0/16"}, want: []string{"10.60.0.0/16", "10.61.0.0/16"}},
		{name: "legacy config without subnets", want: []string{"10.0.0.0/16"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ext := ExternalsCfg{
				NICs: map[string]ExternalNICCfg{},
				VRFs: map[string]ExternalVRFCfg{
					"ext-snp-01": {
						IsStatic: true,
						TableID:  1000,
						StaticCfg: ExternalStaticVrfCfg{
							Prefixes:             []string{"0.0.0.0/0"},
							GatewayIPs:           []string{"100.2.10.0/24"},
							SwitchIP:             "100.2.10.2",
							NICName:              "enp2s2.10",
							IPv4NamespaceSubnets: tt.subnets,
						},
					},
				},
			}

			but, _, err := externalIgnition(fabapi.Fabricator{}, VM{Name: "virt-external"}, ext)
			require.NoError(t, err)

			for _, subnet := range tt.want {
				require.Contains(t, but, "ip route "+subnet+" 100.2.10.2 enp2s2.10\n")
				require.Contains(t, but, "ip route "+subnet+" 100.2.10.2 enp2s2.10 nexthop-vrf ext-snp-01\n")
			}
		})
	}
}

func TestCreateVLABConfigStaticExternalIPv4NamespaceSubnets(t *testing.T) {
	l := apiutil.NewLoader()
	b := &VLABBuilderDefault{
		ExtStaticCount:       1,
		DefaultSwitchProfile: meta.SwitchProfileVS,
	}
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Default()
	require.NoError(t, b.Build(t.Context(), l, f, nil))

	require.NoError(t, l.GetClient().Create(t.Context(), &vpcapi.IPv4Namespace{
		Name:      "ipns-2",
		Namespace: kmetav1.NamespaceDefault,
		Spec:      vpcapi.IPv4NamespaceSpec{Subnets: []string{"10.61.0.0/16"}},
	}))

	cfg, err := createVLABConfig(t.Context(), nil, nil, l.GetClient())
	require.NoError(t, err)

	ipns := &vpcapi.IPv4NamespaceList{}
	require.NoError(t, l.GetClient().List(t.Context(), ipns))
	want := []string{}
	for _, ns := range ipns.Items {
		if ns.Spec.Topology.Fabric == "other" {
			continue
		}
		want = append(want, ns.Spec.Subnets...)
	}
	slices.Sort(want)
	require.Contains(t, want, "10.61.0.0/16")
	require.NotContains(t, want, "10.62.0.0/16")

	require.Len(t, cfg.Externals.VRFs, 1)
	for _, vrf := range cfg.Externals.VRFs {
		require.True(t, vrf.IsStatic)
		require.Equal(t, want, vrf.StaticCfg.IPv4NamespaceSubnets)
	}
}
