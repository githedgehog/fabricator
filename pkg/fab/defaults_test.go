package fab_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"sigs.k8s.io/yaml"
)

func TestInitConfig(t *testing.T) {
	require.Equal(t, false, strings.Contains(string(fab.InitConfigText), "\t"), "InitConfigText should not contain tabs")

	cfg := fabapi.FabConfig{}
	err := yaml.UnmarshalStrict(fab.InitConfigText, &cfg)
	require.NoError(t, err, "InitConfigText should unmarshal without errors")

	expected := fabapi.FabConfig{
		Control: fabapi.ControlConfig{
			TLSSAN: []string{},
			DefaultUser: fabapi.ControlUser{
				AuthorizedKeys: []string{},
			},
		},
		Fabric: fabapi.FabricConfig{
			DefaultSwitchUsers: map[string]fabapi.SwitchUser{
				"admin": {
					AuthorizedKeys: []string{},
					Role:           "admin",
				},
			},
		},
	}
	require.Equal(t, expected, cfg, "InitConfigText should produce empty config")
}
