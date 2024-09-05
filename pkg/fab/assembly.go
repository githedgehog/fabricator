// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fab

import (
	"crypto/x509"
	"fmt"
	"os/user"
	"path/filepath"

	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
	"go.githedgehog.com/fabricator/pkg/fab/vlab"
	fabwiring "go.githedgehog.com/fabricator/pkg/fab/wiring"
)

var (
	HHSubnet                      = "172.30.0.0/16" // All Hedgehog Fabric IPs assignment will happen from this subnet
	ControlKubeClusterCIDR        = "172.28.0.0/16"
	ControlKubeServiceCIDR        = "172.29.0.0/16"
	ControlKubeClusterDNS         = "172.29.0.10"
	ControlVIP                    = "172.30.1.1"
	ControlVIPMask                = "/32"
	ASNSpine               uint32 = 65100
	ASNLeafStart           uint32 = 65101
	ZotCheckURL                   = fmt.Sprintf("https://%s:%d/v2/_catalog", ControlVIP, ZotNodePort)
	K3sAPIPort                    = 6443
	ZotNodePort                   = 31000
	DasBootNTPNodePort            = 30123
	DasBootSyslogNodePort         = 30514
	ControlProxyNodePort          = 31028

	DevSSHKey     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGpF2+9I1Nj4BcN7y6DjzTbq1VcUYIRGyfzId5ZoBEFj" // 1P: Fabric Dev SSH Key Shared
	DevPassword   = "$5$8nAYPGcl4l6G7Av1$Qi4/gnM0yPtGv9kjpMh78NuNSfQWy7vR1rulHpurL36"                  //nolint:gosec // 1P: Fabric Dev SONiC Admin
	DevSonicUsers = []meta.UserCreds{
		{
			Name:     "admin",
			Password: "$5$8nAYPGcl4l6G7Av1$Qi4/gnM0yPtGv9kjpMh78NuNSfQWy7vR1rulHpurL36", // 1P: Fabric Dev SONiC Admin
			Role:     "admin",
		},
		{
			Name:     "op",
			Password: "$5$oj/NxDtFw3eTyini$VHwdjWXSNYRxlFMu.1S5ZlGJbUF/CGmCAZIBroJlax4", // 1P: Fabric Dev SONiC Operator
			Role:     "operator",
		},
	}

	OCIScheme = "oci://"

	// Base
	RefSource          = cnc.Ref{Repo: "ghcr.io/githedgehog"}
	RefTarget          = cnc.Ref{Repo: fmt.Sprintf("%s:%d/githedgehog", ControlVIP, ZotNodePort)}
	RefTargetInCluster = RefTarget

	// K3s
	RefK3s = cnc.Ref{Name: "k3s", Tag: "v1.30.0-k3s1"}

	// Zot
	RefZot            = cnc.Ref{Name: "zot", Tag: "v1.4.3"}
	RefZotTargetImage = cnc.Ref{Repo: "ghcr.io/project-zot", Name: "zot-minimal-linux-amd64"}

	// Das Boot
	DasBootSeederClusterIP = "172.29.42.42"

	RefDasBootVersion      = cnc.Ref{Tag: "v0.12.3"}
	RefDasBootCRDsChart    = cnc.Ref{Name: "das-boot/charts/das-boot-crds"}
	RefDasBootSeederChart  = cnc.Ref{Name: "das-boot/charts/das-boot-seeder"}
	RefDasBootSeederImage  = cnc.Ref{Name: "das-boot/das-boot-seeder"}
	RefDasBootRegCtrlChart = cnc.Ref{Name: "das-boot/charts/das-boot-registration-controller"}
	RefDasBootRegCtrlImage = cnc.Ref{Name: "das-boot/das-boot-registration-controller"}

	RefDasBootRsyslogChart = cnc.Ref{Name: "das-boot/charts/rsyslog", Tag: "0.1.2"}
	RefDasBootRsyslogImage = cnc.Ref{Name: "das-boot/rsyslog", Tag: "0.1.0"}

	RefDasBootNTPChart = cnc.Ref{Name: "das-boot/charts/ntp", Tag: "0.0.3"}
	RefDasBootNTPImage = cnc.Ref{Name: "das-boot/ntp", Tag: "latest"}

	// ONIE
	RefHONIEVersion        = cnc.Ref{Tag: "0.1.3"}
	RefONIETargetVersion   = cnc.Ref{Tag: "latest"} // the target tag currently *must* always be "latest" as this is hardcoded in DAS BOOT
	RefONIESrcTargetsPairs = []struct {
		src     cnc.Ref
		targets []cnc.Ref
	}{
		{
			src:     cnc.Ref{Name: "honie/onie-updater-x86_64-kvm_x86_64-r0"},
			targets: []cnc.Ref{{Name: "onie/onie-updater-x86_64-kvm_x86_64-r0"}},
		},
		// Technically there are more platforms within the AS4630 family.
		// However, our HONIE image will only work on the AS4630-54NPE.
		// The other platforms have even different lane mapping etc. and need to be prepared for
		// first within the platform-accton repository before we can use them.
		// This is why we are creating tags *only* for the 54NPE.
		{
			src:     cnc.Ref{Name: "honie/onie-updater-x86_64-accton_as4630-r0"},
			targets: []cnc.Ref{{Name: "onie/onie-updater-x86_64-accton_as4630_54npe-r0"}},
		},
		{
			src:     cnc.Ref{Name: "honie/onie-updater-x86_64-accton_as7326_56x-r0"},
			targets: []cnc.Ref{{Name: "onie/onie-updater-x86_64-accton_as7326_56x-r0"}},
		},
		{
			src:     cnc.Ref{Name: "honie/onie-updater-x86_64-accton_as7726_32x-r0"},
			targets: []cnc.Ref{{Name: "onie/onie-updater-x86_64-accton_as7726_32x-r0"}},
		},
		// Technically the HONIE image is prepared for *all* the devices in the S5200 family.
		// This is why we are creating tags for all of the platforms already.
		// However, officially we are only supporting the 5232 and 5248.
		{
			src: cnc.Ref{Name: "honie/onie-updater-x86_64-dellemc_s5200_c3538-r0"},
			targets: []cnc.Ref{
				// {Name: "onie/onie-updater-x86_64-dellemc_s5200_c3538-r0"},
				// {Name: "onie/onie-updater-x86_64-dellemc_s5212f_c3538-r0"},
				// {Name: "onie/onie-updater-x86_64-dellemc_s5224f_c3538-r0"},
				{Name: "onie/onie-updater-x86_64-dellemc_s5232f_c3538-r0"},
				{Name: "onie/onie-updater-x86_64-dellemc_s5248f_c3538-r0"},
				// {Name: "onie/onie-updater-x86_64-dellemc_s5296f_c3538-r0"},
			},
		},
	}

	// SONiC
	RefSonicBCMBase   = cnc.Ref{Name: "sonic-bcom-private", Tag: "base-bin-4.4.0"}
	RefSonicBCMCampus = cnc.Ref{Name: "sonic-bcom-private", Tag: "campus-bin-4.4.0"}
	RefSonicBCMVS     = cnc.Ref{Name: "sonic-bcom-private", Tag: "vs-bin-4.4.0"}

	RefSonicTargetVersion = cnc.Ref{Tag: "latest"}
	RefSonicTargetsBase   = []cnc.Ref{
		{Name: "sonic/x86_64-dellemc_s5248f_c3538-r0"}, // Dell S5248
		{Name: "sonic/x86_64-dellemc_s5232f_c3538-r0"}, // Dell S5232
		{Name: "sonic/x86_64-cel_questone_2-r0"},       // Celestica DS2000
		{Name: "sonic/x86_64-cel_seastone_2-r0"},       // Celestica DS3000
		{Name: "sonic/x86_64-cel_silverstone-r0"},      // Celestica DS4000
		{Name: "sonic/x86_64-accton_as7726_32x-r0"},    // EdgeCore DCS204
		{Name: "sonic/x86_64-accton_as7326_56x-r0"},    // EdgeCore DCS203
		{Name: "sonic/x86_64-accton_as7712_32x-r0"},    // Edgecore AS7712-32X
	}
	RefSonicTargetsCampus = []cnc.Ref{
		{Name: "sonic/x86_64-accton_as4630_54npe-r0"}, // EdgeCore EPS202
	}
	RefSonicTargetsVS = []cnc.Ref{
		{Name: "sonic/x86_64-kvm_x86_64-r0"}, // VS
	}

	// Fabric
	RefFabricVersion         = cnc.Ref{Tag: "v0.45.1"}
	RefFabricAPIChart        = cnc.Ref{Name: "fabric/charts/fabric-api"}
	RefFabricChart           = cnc.Ref{Name: "fabric/charts/fabric"}
	RefFabricImage           = cnc.Ref{Name: "fabric/fabric"}
	RefFabricAgent           = cnc.Ref{Name: "fabric/agent"}
	RefFabricControlAgent    = cnc.Ref{Name: "fabric/agent"}
	RefFabricCtl             = cnc.Ref{Name: "fabric/hhfctl"}
	RefFabricDHCPServer      = cnc.Ref{Name: "fabric/fabric-dhcp-server"}
	RefFabricDHCPServerChart = cnc.Ref{Name: "fabric/charts/fabric-dhcp-server"}
	RefFabricDHCPD           = cnc.Ref{Name: "fabric/fabric-dhcpd"}
	RefFabricDHCPDChart      = cnc.Ref{Name: "fabric/charts/fabric-dhcpd"}
	RefAlloy                 = cnc.Ref{Name: "fabric/alloy", Tag: "v1.1.1"}
	RefControlProxy          = cnc.Ref{Name: "fabric/fabric-proxy", Tag: "1.9.1"}
	RefControlProxyChart     = cnc.Ref{Name: "fabric/charts/fabric-proxy"}

	// Misc
	RefK9s       = cnc.Ref{Name: "fabricator/k9s", Tag: "v0.32.4"}
	RefRBACProxy = cnc.Ref{Name: "fabricator/kube-rbac-proxy", Tag: "v0.14.1"}
	RefToolbox   = cnc.Ref{Name: "fabricator/toolbox", Tag: "latest"}

	// Cert manager
	RefCertManagerVersion    = cnc.Ref{Tag: "v1.13.0"}
	RefCertManagerCAInjector = cnc.Ref{Name: "fabricator/cert-manager-cainjector"}
	RefCertManagerController = cnc.Ref{Name: "fabricator/cert-manager-controller"}
	RefCertManagerACMESolver = cnc.Ref{Name: "fabricator/cert-manager-acmesolver"}
	RefCertManagerWebhook    = cnc.Ref{Name: "fabricator/cert-manager-webhook"}
	RefCertManagerCtl        = cnc.Ref{Name: "fabricator/cert-manager-ctl"}
	RefCertManagerChart      = cnc.Ref{Name: "fabricator/charts/cert-manager"}

	// Reloader
	RefMiscReloader      = cnc.Ref{Name: "fabricator/reloader", Tag: "v1.0.40"}
	RefMiscReloaderChart = cnc.Ref{Name: "fabricator/charts/reloader", Tag: "1.0.40"}

	// VLAB
	RefVLABONIE       = cnc.Ref{Name: "honie", Tag: "lldp"}
	RefVLABFlatcar    = cnc.Ref{Name: "flatcar", Tag: "3815.2.2"}
	RefVLABEEPROMEdit = cnc.Ref{Name: "onie-qcow2-eeprom-edit", Tag: "latest"}

	//Logan Testing
	RefLiveImageTree = cnc.Ref{Repo: "ghcr.io/mrbojangles3", Name: "control-iso-root", Tag: "0.5"}
	RefOEMCpio       = cnc.Ref{Repo: "ghcr.io/mrbojangles3", Name: "hedgehog-oem-cpio", Tag: "latest"}
)

const (
	CategoryConfigBaseSuffix = " fabricator options:"
)

const (
	PresetBM   cnc.Preset = "lab"
	PresetVLAB cnc.Preset = "vlab"
)

var Presets = []cnc.Preset{PresetBM, PresetVLAB}

var (
	BundleControlInstall = cnc.Bundle{
		Name:        "control-install",
		IsInstaller: true,
	}
	BundleControlOS = cnc.Bundle{
		Name: "control-os",
	}
	BundleServerInstall = cnc.Bundle{
		Name:        "server-install",
		IsInstaller: true,
	}
	BundleServerOS = cnc.Bundle{
		Name: "server-os",
	}
	BundleVlabFiles = cnc.Bundle{
		Name: "vlab-files",
	}
	BundleVlabVMs = cnc.Bundle{ // Special case, just to keep name
		Name: "vlab-vms",
	}
	BundleHlabFiles = cnc.Bundle{ // Special case, just to keep name
		Name: "hlab-files",
	}
	BundleControlISO = cnc.Bundle{
		Name:         "control-iso",
		IsISOBuilder: true,
	}
)

// We expect services installed during the stage to be available at the end of it
const (
	Stage                cnc.Stage = iota // Just a placeholder stage
	StageInstall0Prep                     // Preparation for K3s and Zot installation
	StageInstall1K3sZot                   // Kube and Registry Installation, wait for registry available
	StageInstall2Misc                     // Install misc services and wait for them to be ready
	StageInstall3Fabric                   // Install Fabric and wait for it to be ready
	StageInstall4DasBoot                  // Install Das Boot and wait for it to be ready
	StageInstall9Reloader

	StageMax // Keep it last so we can iterate over all stages
)

func NewCNCManager() *cnc.Manager {
	return cnc.New(
		Presets,
		[]cnc.Bundle{BundleControlInstall, BundleControlOS, BundleControlISO, BundleServerInstall, BundleServerOS, BundleVlabFiles},
		StageMax,
		[]cnc.Component{
			&Base{},
			&ControlOS{},
			&K3s{},
			&Zot{},
			&Misc{},
			&DasBoot{},
			&Fabric{},
			&VLAB{},
			&ServerOS{},
		},
		&fabwiring.HydrateConfig{
			Subnet:       HHSubnet,
			SpineASN:     ASNSpine,
			LeafASNStart: ASNLeafStart,
		},
	)
}

const (
	OCIRepoCACN     = "OCI Repository CA"
	OCIRepoServerCN = "localhost"

	KeyUsageCA     = x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign
	KeyUsageServer = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
)

func LoadVLAB(basedir string, mngr *cnc.Manager, dryRun bool, size string, restrictServers bool) (*vlab.Service, error) {
	if mngr.Preset() != PresetVLAB {
		return nil, errors.Errorf("only vlab preset supported, found %s", mngr.Preset())
	}

	sudoSwtpm := false
	u, err := user.Current()
	if err != nil && u.Username == "runner" { // quick hack for GHA with self-hosted runners
		sudoSwtpm = true
	}

	svc, err := vlab.Load(&vlab.ServiceConfig{
		DryRun:            dryRun,
		Size:              size,
		SudoSwtpm:         sudoSwtpm,
		Basedir:           filepath.Join(basedir, BundleVlabVMs.Name),
		Wiring:            mngr.Wiring(),
		ControlIgnition:   filepath.Join(basedir, BundleControlOS.Name, ControlOSIgnition),
		ServerIgnitionDir: filepath.Join(basedir, BundleServerOS.Name),
		ControlInstaller:  filepath.Join(basedir, BundleControlInstall.Name),
		ServerInstaller:   filepath.Join(basedir, BundleServerInstall.Name),
		RestrictServers:   restrictServers,
		FilesDir:          filepath.Join(basedir, BundleVlabFiles.Name),
		SSHKey:            filepath.Join(basedir, DefaultVLABSSHKey),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "error loading VLAB")
	}

	return svc, nil
}
