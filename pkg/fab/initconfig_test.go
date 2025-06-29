// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab_test

import (
	"testing"

	"dario.cat/mergo"
	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInitConfig(t *testing.T) {
	ctx := t.Context()

	for _, test := range []struct {
		name        string
		in          fab.InitConfigInput
		expectedFab fabapi.Fabricator
		initErr     bool
		validErr    bool
	}{
		{
			name:     "default",
			in:       fab.InitConfigInput{},
			validErr: true,
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
			initErr: true,
		},
		{
			name: "include-onie-import-upstream",
			in: fab.InitConfigInput{
				DefaultPasswordHash: "$5$bar",
				IncludeONIE:         true,
				RegUpstream: &fabapi.ControlConfigRegistryUpstream{
					Repo:        "repo",
					Prefix:      "prefix",
					NoTLSVerify: true,
					Username:    "username",
					Password:    "password",
				},
			},
			expectedFab: fabapi.Fabricator{
				ObjectMeta: metav1.ObjectMeta{
					Name:      comp.FabName,
					Namespace: comp.FabNamespace,
				},
				Spec: fabapi.FabricatorSpec{
					Config: fabapi.FabConfig{
						Registry: fabapi.RegistryConfig{
							Mode: fabapi.RegistryModeUpstream,
							Upstream: &fabapi.ControlConfigRegistryUpstream{
								Repo:        "repo",
								Prefix:      "prefix",
								NoTLSVerify: true,
								Username:    "username",
								Password:    "password",
							},
						},
						Control: fabapi.ControlConfig{
							DefaultUser: fabapi.ControlUser{
								PasswordHash: "$5$bar",
							},
						},
						Fabric: fabapi.FabricConfig{
							IncludeONIE: true,
							DefaultSwitchUsers: map[string]fabapi.SwitchUser{
								"admin": {
									Role:         "admin",
									PasswordHash: "$5$bar",
								},
								"op": {
									Role:         "operator",
									PasswordHash: "$5$bar",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "control-node-management-link",
			in: fab.InitConfigInput{
				DefaultPasswordHash:       "$5$bar",
				ControlNodeManagementLink: "pci@0000:00:00.0",
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
								PasswordHash: "$5$bar",
							},
						},
						Fabric: fabapi.FabricConfig{
							DefaultSwitchUsers: map[string]fabapi.SwitchUser{
								"admin": {
									Role:         "admin",
									PasswordHash: "$5$bar",
								},
								"op": {
									Role:         "operator",
									PasswordHash: "$5$bar",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "gateway",
			in: fab.InitConfigInput{
				Dev:     true,
				Gateway: true,
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
						Gateway: fabapi.GatewayConfig{
							Enable: true,
						},
					},
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			expectedControls := []fabapi.ControlNode{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "control-1",
						Namespace: comp.FabNamespace,
					},
					Spec: fabapi.ControlNodeSpec{
						Bootstrap: fabapi.ControlNodeBootstrap{
							Disk: "/dev/sda",
						},
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
			if test.in.ControlNodeManagementLink != "" {
				expectedControls[0].Annotations = map[string]string{
					"link.hhfab.githedgehog.com/enp2s1": test.in.ControlNodeManagementLink,
				}
			}

			expectedNodes := []fabapi.FabNode{}
			if test.in.Gateway {
				expectedNodes = append(expectedNodes, fabapi.FabNode{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-1",
						Namespace: comp.FabNamespace,
					},
					Spec: fabapi.FabNodeSpec{
						Roles: []fabapi.FabNodeRole{fabapi.NodeRoleGateway},
						Bootstrap: fabapi.ControlNodeBootstrap{
							Disk: "/dev/sda",
						},
						Management: fabapi.ControlNodeManagement{
							Interface: "enp2s0",
						},
					},
				})
			}

			test.expectedFab.Default()
			test.expectedFab.Spec.Config.Fabric.LoopbackWorkaroundDisable = true

			data, err := fab.InitConfig(ctx, test.in)
			if test.initErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)

			l := apiutil.NewLoader()
			require.NoError(t, l.LoadAdd(ctx, apiutil.FabricatorGVKs, data))

			f, controls, nodes, err := fab.GetFabAndNodes(ctx, l.GetClient(), fab.GetFabAndNodesOpts{AllowNotHydrated: true})
			if test.validErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)

			f.APIVersion = ""
			f.Kind = ""
			f.ResourceVersion = ""
			f.Status = fabapi.FabricatorStatus{}

			require.NotNil(t, controls)

			for i := range controls {
				controls[i].APIVersion = ""
				controls[i].Kind = ""
				controls[i].ResourceVersion = ""
				controls[i].Status = fabapi.ControlNodeStatus{}
			}

			for i := range nodes {
				nodes[i].APIVersion = ""
				nodes[i].Kind = ""
				nodes[i].ResourceVersion = ""
				nodes[i].Status = fabapi.FabNodeStatus{}
			}

			expectedFab := test.expectedFab
			if err := mergo.Merge(&expectedFab.Spec.Config, *fab.DefaultConfig.DeepCopy()); err != nil {
				require.NoError(t, err)
			}

			require.Equal(t, expectedFab, f)
			require.Equal(t, expectedControls, controls)
			require.Equal(t, expectedNodes, nodes)
		})
	}
}
