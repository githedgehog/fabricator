package fab_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInitConfig(t *testing.T) {
	ctx := context.Background()
	expectedControls := []fabapi.ControlNode{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "control-1",
				Namespace: comp.FabNamespace,
			},
			Spec: fabapi.ControlNodeSpec{
				Management: fabapi.ControlNodeManagement{
					Interface: "enp2s1",
				},
				External: fabapi.ControlNodeExternal{
					Interface: "enp2s0",
					IP:        meta.PrefixDHCP,
				},
			},
		},
	}

	for _, test := range []struct {
		name        string
		in          fab.InitConfigInput
		expectedFab fabapi.Fabricator
		err         bool
	}{
		{
			name: "default",
			in:   fab.InitConfigInput{},
			expectedFab: fabapi.Fabricator{
				ObjectMeta: metav1.ObjectMeta{
					Name:      comp.FabName,
					Namespace: comp.FabNamespace,
				},
				Spec: fabapi.FabricatorSpec{
					Config: fabapi.FabConfig{
						Fabric: fabapi.FabricConfig{
							DefaultSwitchUsers: map[string]fabapi.SwitchUser{
								"admin": {
									Role: "admin",
								},
								"op": {
									Role: "operator",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "dev",
			in: fab.InitConfigInput{
				Dev: true,
			},
			expectedFab: fabapi.Fabricator{
				ObjectMeta: metav1.ObjectMeta{
					Name:      comp.FabName,
					Namespace: comp.FabNamespace,
				},
				Spec: fabapi.FabricatorSpec{
					Config: fabapi.FabConfig{
						Control: fabapi.ControlConfig{
							DefaultUser: fabapi.ControlUser{
								PasswordHash:   fab.DevAdminPasswordHash,
								AuthorizedKeys: []string{fab.DevSSHKey},
							},
						},
						Fabric: fabapi.FabricConfig{
							DefaultSwitchUsers: map[string]fabapi.SwitchUser{
								"admin": {
									Role:           "admin",
									PasswordHash:   fab.DevAdminPasswordHash,
									AuthorizedKeys: []string{fab.DevSSHKey},
								},
								"op": {
									Role:           "operator",
									PasswordHash:   fab.DevAdminPasswordHash,
									AuthorizedKeys: []string{fab.DevSSHKey},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "input",
			in: fab.InitConfigInput{
				TLSSAN:                []string{"foo"},
				DefaultPasswordHash:   "$5$bar",
				DefaultAuthorizedKeys: []string{"baz"},
			},
			expectedFab: fabapi.Fabricator{
				ObjectMeta: metav1.ObjectMeta{
					Name:      comp.FabName,
					Namespace: comp.FabNamespace,
				},
				Spec: fabapi.FabricatorSpec{
					Config: fabapi.FabConfig{
						Control: fabapi.ControlConfig{
							TLSSAN: []string{"foo"},
							DefaultUser: fabapi.ControlUser{
								PasswordHash:   "$5$bar",
								AuthorizedKeys: []string{"baz"},
							},
						},
						Fabric: fabapi.FabricConfig{
							DefaultSwitchUsers: map[string]fabapi.SwitchUser{
								"admin": {
									Role:           "admin",
									PasswordHash:   "$5$bar",
									AuthorizedKeys: []string{"baz"},
								},
								"op": {
									Role:           "operator",
									PasswordHash:   "$5$bar",
									AuthorizedKeys: []string{"baz"},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "input-dev",
			in: fab.InitConfigInput{
				TLSSAN:                []string{"foo"},
				DefaultAuthorizedKeys: []string{"baz"},
				Dev:                   true,
			},
			expectedFab: fabapi.Fabricator{
				ObjectMeta: metav1.ObjectMeta{
					Name:      comp.FabName,
					Namespace: comp.FabNamespace,
				},
				Spec: fabapi.FabricatorSpec{
					Config: fabapi.FabConfig{
						Control: fabapi.ControlConfig{
							TLSSAN: []string{"foo"},
							DefaultUser: fabapi.ControlUser{
								PasswordHash:   fab.DevAdminPasswordHash,
								AuthorizedKeys: []string{"baz", fab.DevSSHKey},
							},
						},
						Fabric: fabapi.FabricConfig{
							DefaultSwitchUsers: map[string]fabapi.SwitchUser{
								"admin": {
									Role:           "admin",
									PasswordHash:   fab.DevAdminPasswordHash,
									AuthorizedKeys: []string{"baz", fab.DevSSHKey},
								},
								"op": {
									Role:           "operator",
									PasswordHash:   fab.DevAdminPasswordHash,
									AuthorizedKeys: []string{"baz", fab.DevSSHKey},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "passwd-dev",
			in: fab.InitConfigInput{
				DefaultPasswordHash: "$5$bar",
				Dev:                 true,
			},
			err: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := fab.InitConfig(ctx, test.in)
			if test.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)

			l := apiutil.NewFabLoader()
			require.NoError(t, l.LoadAdd(ctx, data))

			fab, controls, err := fab.GetFabAndControls(ctx, l.GetClient(), true)
			require.NoError(t, err)

			fab.APIVersion = ""
			fab.Kind = ""
			fab.ResourceVersion = ""

			require.NotNil(t, controls)

			for i := range controls {
				controls[i].APIVersion = ""
				controls[i].Kind = ""
				controls[i].ResourceVersion = ""
			}

			require.Equal(t, test.expectedFab, fab)
			require.Equal(t, expectedControls, controls)
		})
	}
}
