package fab

import (
	"strings"

	fmeta "go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

// TODO more comments, instructions on how to generate password hashes, etc.
var InitConfigText = []byte(strings.TrimSpace(`
# Fabricator configuration file

control:
  tlsSAN: [ # add IP addresses or DNS names that will be used to access API
    # "fabric.local"
  ]

  defaultUser: # user 'core' on all control nodes
    # password: "$5$8nAYPGcl4..." # password hash
    authorizedKeys: [ # optionally add your SSH public keys here
      # "ssh-ed25519 ..."
    ]

fabric:
  defaultSwitchUsers:
    admin: # at least one user with name 'admin' and role 'admin' is required
      authorizedKeys: [ # optionally add your SSH public keys here
       # ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGpF2+9I1Nj4BcN7y6DjzTbq1VcUYIRGyfzId5ZoBEFj
      ]
      # password: "$5$8nAYPGcl4..." # password hash
      role: admin # user role, 'admin' or 'operator' (read-only)

# For more configuration options see https://docs.githedgehog.com
`))

var DefaultConfig = fabapi.FabConfig{
	Control: fabapi.ControlConfig{
		ManagementSubnet:  "172.30.1.0/24",
		VIP:               "172.30.1.1",
		TLSSAN:            []string{}, // TODO
		KubeClusterSubnet: "172.28.0.0/16",
		KubeServiceSubnet: "172.29.0.0/16",
		KubeClusterDNS:    "172.29.0.10",
		DefaultUser:       fabapi.ControlUser{},
	},
	Registry: fabapi.RegistryConfig{},
	Fabric: fabapi.FabricConfig{
		SpineASN:            65100,
		LeafASNStart:        65101,
		LeafASNEnd:          65999,
		ProtocolSubnet:      "TBD",               // TODO
		VTEPSubnet:          "TBD",               // TODO
		FabricSubnet:        "TBD",               // TODO
		BaseVPCCommunity:    "50000:0",           // TODO make sure it's really used
		VPCIRBVLANs:         []fmeta.VLANRange{}, // TODO
		VPCWorkaroundVLANs:  []fmeta.VLANRange{}, // TODO
		VPCWorkaroundSubnet: "172.30.240.0/20",   // TODO make sure it's really used
		ESLAGMACBase:        "f2:00:00:00:00:00", // TODO make sure it's really used
		ESLAGESIPrefix:      "00:f2:00:00:",      // TODO make sure it's really used
		DefaultSwitchUsers:  map[string]fabapi.SwitchUser{},
	},
}

const DevSSHKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGpF2+9I1Nj4BcN7y6DjzTbq1VcUYIRGyfzId5ZoBEFj"

var DevConfig = fabapi.FabConfig{
	Control: fabapi.ControlConfig{
		DefaultUser: fabapi.ControlUser{
			PasswordHash:   "$5$8nAYPGcl4l6G7Av1$Qi4/gnM0yPtGv9kjpMh78NuNSfQWy7vR1rulHpurL36",
			AuthorizedKeys: []string{DevSSHKey},
		},
	},
	Fabric: fabapi.FabricConfig{
		DefaultSwitchUsers: map[string]fabapi.SwitchUser{
			"admin": {
				PasswordHash:   "$5$8nAYPGcl4l6G7Av1$Qi4/gnM0yPtGv9kjpMh78NuNSfQWy7vR1rulHpurL36",
				Role:           "admin",
				AuthorizedKeys: []string{DevSSHKey},
			},
			"op": {
				PasswordHash:   "$5$oj/NxDtFw3eTyini$VHwdjWXSNYRxlFMu.1S5ZlGJbUF/CGmCAZIBroJlax4",
				Role:           "operator",
				AuthorizedKeys: []string{DevSSHKey},
			},
		},
	},
}
