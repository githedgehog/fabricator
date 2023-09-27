package fab

import (
	"crypto/x509"
	"fmt"
	"path/filepath"

	"github.com/pkg/errors"
	agentapi "go.githedgehog.com/fabric/api/agent/v1alpha2"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
	"go.githedgehog.com/fabricator/pkg/fab/vlab"
)

var (
	CONTROL_VIP      = "192.168.42.1"
	CONTROL_VIP_MASK = "/32"
	ZOT_CHECK_URL    = fmt.Sprintf("https://%s:%d/v2/_catalog", CONTROL_VIP, ZOT_NODE_PORT)
	K3S_API_PORT     = 6443
	ZOT_NODE_PORT    = 31000
	VPC_VLAN_MIN     = 1000
	VPC_VLAN_MAX     = 1999
	DEFAULT_USERS    = []agentapi.UserCreds{
		{
			Name:     "admin",
			Password: "$5$8nAYPGcl4l6G7Av1$Qi4/gnM0yPtGv9kjpMh78NuNSfQWy7vR1rulHpurL36",
			Role:     "admin",
		},
		{
			Name:     "op",
			Password: "$5$oj/NxDtFw3eTyini$VHwdjWXSNYRxlFMu.1S5ZlGJbUF/CGmCAZIBroJlax4",
			Role:     "operator",
		},
	}

	// Base
	REF_SOURCE           = cnc.Ref{Repo: "ghcr.io/githedgehog"}
	REF_TARGET           = cnc.Ref{Repo: fmt.Sprintf("%s:%d/githedgehog", CONTROL_VIP, ZOT_NODE_PORT)}
	REF_TARGET_INCLUSTER = REF_TARGET

	// K3s
	REF_K3S = cnc.Ref{Name: "k3s", Tag: "v1.27.4-k3s1"}

	// Zot
	REF_ZOT              = cnc.Ref{Name: "zot", Tag: "v1.4.3"}
	REF_ZOT_TARGET_IMAGE = cnc.Ref{Repo: "ghcr.io/project-zot", Name: "zot-minimal-linux-amd64"}

	// Das Boot
	REF_DASBOOT_VERSION       = cnc.Ref{Tag: "v0.7.3"}
	REF_DASBOOT_CRDS_CHART    = cnc.Ref{Name: "das-boot/charts/das-boot-crds"}
	REF_DASBOOT_SEEDER_CHART  = cnc.Ref{Name: "das-boot/charts/das-boot-seeder"}
	REF_DASBOOT_SEEDER_IMAGE  = cnc.Ref{Name: "das-boot/das-boot-seeder"}
	REF_DASBOOT_REGCTRL_CHART = cnc.Ref{Name: "das-boot/charts/das-boot-registration-controller"}
	REF_DASBOOT_REGCTRL_IMAGE = cnc.Ref{Name: "das-boot/das-boot-registration-controller"}

	REF_DASBOOT_RSYSLOG_CHART = cnc.Ref{Name: "das-boot/charts/rsyslog", Tag: "0.1.1"}
	REF_DASBOOT_RSYSLOG_IMAGE = cnc.Ref{Name: "das-boot/rsyslog", Tag: "0.1.0"}

	REF_DASBOOT_NTP_CHART = cnc.Ref{Name: "das-boot/charts/ntp", Tag: "0.0.1"}
	REF_DASBOOT_NTP_IMAGE = cnc.Ref{Name: "das-boot/ntp", Tag: "latest"}

	// SONiC
	REF_SONIC_BCOM_BASE = cnc.Ref{Name: "sonic-bcom-private", Tag: "base-bin-4.1.1"}
	REF_SONIC_BCOM_VLAB = cnc.Ref{Name: "sonic-bcom-private", Tag: "vs-bin-4.1.1"}

	REF_SONIC_TARGET_VERSION  = cnc.Ref{Tag: "latest"}
	REF_SONIC_TARGETS_DEFAULT = []cnc.Ref{
		{Name: "sonic/x86_64-dellemc_s5248f_c3538-r0"},
		{Name: "sonic/x86_64-cel_seastone_2-r0"},
	}
	REF_SONIC_TARGETS_VLAB = []cnc.Ref{
		{Name: "sonic/x86_64-kvm_x86_64-r0"},
	}

	// Fabric
	REF_FABRIC_VERSION           = cnc.Ref{Tag: "v0.16.6"}
	REF_FABRIC_API_CHART         = cnc.Ref{Name: "fabric/charts/fabric-api"}
	REF_FABRIC_CHART             = cnc.Ref{Name: "fabric/charts/fabric"}
	REF_FABRIC_IMAGE             = cnc.Ref{Name: "fabric/fabric"}
	REF_FABRIC_AGENT             = cnc.Ref{Name: "fabric/agent"}
	REF_FABRIC_CTL               = cnc.Ref{Name: "fabric/hhfctl"}
	REF_FABRIC_DHCP_SERVER       = cnc.Ref{Name: "fabric/fabric-dhcp-server"}
	REF_FABRIC_DHCP_SERVER_CHART = cnc.Ref{Name: "fabric/charts/fabric-dhcp-server"}

	// Misc
	REF_K9S        = cnc.Ref{Name: "fabricator/k9s", Tag: "v0.27.4"}
	REF_RBAC_PROXY = cnc.Ref{Name: "fabricator/kube-rbac-proxy", Tag: "v0.14.1"}

	// Cert manager
	REF_CERT_MANAGER_VERSION    = cnc.Ref{Tag: "v1.13.0"}
	REF_CERT_MANAGER_CAINJECTOR = cnc.Ref{Name: "fabricator/cert-manager-cainjector"}
	REF_CERT_MANAGER_CONTROLLER = cnc.Ref{Name: "fabricator/cert-manager-controller"}
	REF_CERT_MANAGER_ACMESOLVER = cnc.Ref{Name: "fabricator/cert-manager-acmesolver"}
	REF_CERT_MANAGER_WEBHOOK    = cnc.Ref{Name: "fabricator/cert-manager-webhook"}
	REF_CERT_MANAGER_CTL        = cnc.Ref{Name: "fabricator/cert-manager-ctl"}
	REF_CERT_MANAGER_CHART      = cnc.Ref{Name: "fabricator/charts/cert-manager"}

	// Reloader
	REF_MISC_RELOADER       = cnc.Ref{Name: "fabricator/reloader", Tag: "v1.0.40"}
	REF_MISC_RELOADER_CHART = cnc.Ref{Name: "fabricator/charts/reloader", Tag: "1.0.40"}

	// VLAB
	REF_VLAB_ONIE        = cnc.Ref{Name: "honie", Tag: "dhcp-removed"}
	REF_VLAB_FLATCAR     = cnc.Ref{Name: "flatcar", Tag: "3510.2.1"}
	REF_VLAB_EEPROM_EDIT = cnc.Ref{Name: "onie-qcow2-eeprom-edit", Tag: "latest"}
)

const (
	PRESET_BM   cnc.Preset = "lab"
	PRESET_VLAB cnc.Preset = "vlab"
)

var Presets = []cnc.Preset{PRESET_BM, PRESET_VLAB}

var (
	BundleControlInstall = cnc.Bundle{
		Name:        "control-install",
		IsInstaller: true,
	}
	BundleControlOS = cnc.Bundle{
		Name: "control-os",
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
)

// We expect services installed during the stage to be available at the end of it
const (
	STAGE                   cnc.Stage = iota // Just a placeholder stage
	STAGE_INSTALL_0_PREP                     // Preparation for K3s and Zot installation
	STAGE_INSTALL_1_K3SZOT                   // Kube and Registry Installation, wait for registry available
	STAGE_INSTALL_2_MISC                     // Install misc services and wait for them to be ready
	STAGE_INSTALL_3_FABRIC                   // Install Fabric and wait for it to be ready
	STAGE_INSTALL_4_DASBOOT                  // Install Das Boot and wait for it to be ready

	STAGE_MAX // Keep it last so we can iterate over all stages
)

func NewCNCManager() *cnc.Manager {
	return cnc.New(
		Presets,
		[]cnc.Bundle{BundleControlInstall, BundleControlOS, BundleVlabFiles, BundleServerOS},
		STAGE_MAX,
		[]cnc.Component{
			&Base{},
			&ControlOS{},
			&K3s{},
			&Zot{},
			&Misc{},
			&DasBoot{},
			&Fabric{},
			&VLAB{},
		},
	)
}

const (
	OCI_REPO_CA_CN     = "OCI Repository CA"
	OCI_REPO_SERVER_CN = "localhost"

	KEY_USAGE_CA     = x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign
	KEY_USAGE_SERVER = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
)

func LoadVLAB(basedir string, mngr *cnc.Manager, dryRun bool) (*vlab.Service, error) {
	if mngr.Preset() != PRESET_VLAB {
		return nil, errors.Errorf("only vlab preset supported, found %s", mngr.Preset())
	}

	svc, err := vlab.Load(&vlab.Config{
		DryRun:            dryRun,
		Basedir:           filepath.Join(basedir, BundleVlabVMs.Name),
		Wiring:            mngr.Wiring(),
		ControlIgnition:   filepath.Join(basedir, BundleControlOS.Name, CONTROL_OS_IGNITION),
		ServerIgnitionDir: filepath.Join(basedir, BundleServerOS.Name),
		ControlInstaller:  filepath.Join(basedir, BundleControlInstall.Name),
		FilesDir:          filepath.Join(basedir, BundleVlabFiles.Name),
		SshKey:            filepath.Join(basedir, DEFAULT_SSH_KEY),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "error loading VLAB")
	}

	return svc, nil
}
