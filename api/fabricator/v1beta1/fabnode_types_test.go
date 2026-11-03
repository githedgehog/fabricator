// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
)

func TestFabNodeHugePages(t *testing.T) {
	for _, tt := range []struct {
		name    string
		anns    map[string]string
		pages1G int
		pages2M int
		err     bool
	}{
		{
			name:    "defaults",
			pages1G: DefaultHugePages1G,
			pages2M: DefaultHugePages2M,
		},
		{
			name:    "both",
			anns:    map[string]string{gwapi.AnnotationHugePages1G: "4", gwapi.AnnotationHugePages2M: "512"},
			pages1G: 4,
			pages2M: 512,
		},
		{
			name:    "only-2m",
			anns:    map[string]string{gwapi.AnnotationHugePages1G: "0", gwapi.AnnotationHugePages2M: "1024"},
			pages2M: 1024,
		},
		{
			name: "negative",
			anns: map[string]string{gwapi.AnnotationHugePages1G: "-1"},
			err:  true,
		},
		{
			name: "not-a-number",
			anns: map[string]string{gwapi.AnnotationHugePages2M: "1Gi"},
			err:  true,
		},
		{
			name: "too-many",
			anns: map[string]string{gwapi.AnnotationHugePages1G: "1025"},
			err:  true,
		},
		{
			name: "same-count",
			anns: map[string]string{gwapi.AnnotationHugePages1G: "4", gwapi.AnnotationHugePages2M: "4"},
			err:  true,
		},
		{
			name:    "same-count-none",
			anns:    map[string]string{gwapi.AnnotationHugePages1G: "0", gwapi.AnnotationHugePages2M: "0"},
			pages1G: 0,
			pages2M: 0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pages1G, pages2M, err := (&FabNode{Annotations: tt.anns}).HugePages()
			if tt.err {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.pages1G, pages1G)
			require.Equal(t, tt.pages2M, pages2M)
		})
	}
}
