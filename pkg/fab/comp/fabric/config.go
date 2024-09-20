package fabric

import (
	"fmt"

	"go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
)

func GetFabricConfig(f fabapi.Fabricator) (*meta.FabricConfig, error) {
	// TODO align APIs (user creds)
	users := []meta.UserCreds{}
	for name, user := range f.Spec.Config.Fabric.DefaultSwitchUsers {
		users = append(users, meta.UserCreds{
			Name:     name,
			Role:     user.Role,
			Password: user.PasswordHash,
			SSHKeys:  user.AuthorizedKeys,
		})
	}

	// TODO align APIs (fabric config field names, check agent spec too)
	return &meta.FabricConfig{
		ControlVIP:           fmt.Sprintf("%s/32", f.Spec.Config.Control.VIP),
		APIServer:            fmt.Sprintf("%s:%d", f.Spec.Config.Control.VIP, k3s.APIPort),
		AgentRepo:            "", // TODO
		AgentRepoCA:          "", // TODO autoread from CM/Secret?
		VPCIRBVLANRanges:     f.Spec.Config.Fabric.VPCIRBVLANs,
		VPCPeeringVLANRanges: f.Spec.Config.Fabric.VPCWorkaroundVLANs,
		VPCPeeringDisabled:   false, // TODO remove?
		ReservedSubnets: []string{
			// TODO what else?
			string(f.Spec.Config.Control.ManagementSubnet),
			string(f.Spec.Config.Fabric.FabricSubnet),
			string(f.Spec.Config.Fabric.ProtocolSubnet),
			string(f.Spec.Config.Fabric.VTEPSubnet),
			string(f.Spec.Config.Fabric.VPCWorkaroundSubnet),
		},
		Users:                    users,
		DHCPMode:                 meta.DHCPModeHedgehog, // TODO remove
		DHCPDConfigMap:           "",                    // TODO remove
		DHCPDConfigKey:           "",                    // TODO remove
		FabricMode:               f.Spec.Config.Fabric.Mode,
		BaseVPCCommunity:         f.Spec.Config.Fabric.BaseVPCCommunity,
		VPCLoopbackSubnet:        string(f.Spec.Config.Fabric.VPCWorkaroundSubnet),
		FabricMTU:                9100, // TODO use
		ServerFacingMTUOffset:    64,   // TODO use
		ESLAGMACBase:             f.Spec.Config.Fabric.ESLAGMACBase,
		ESLAGESIPrefix:           f.Spec.Config.Fabric.ESLAGESIPrefix,
		AlloyRepo:                "",                 // TODO
		AlloyVersion:             "",                 // TODO
		Alloy:                    meta.AlloyConfig{}, // TODO
		DefaultMaxPathsEBGP:      64,
		AllowExtraSwitchProfiles: false,
	}, nil
}
