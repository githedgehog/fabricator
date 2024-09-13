package meta_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/api/meta"
)

func TestVersionValidate(t *testing.T) {
	for _, test := range []struct {
		v   meta.Version
		err bool
	}{
		{v: "1", err: true},
		{v: "1.0", err: true},
		{v: "1.0.0", err: true},
		{v: "v1"},
		{v: "v1.0"},
		{v: "v1.0.0"},
		{v: "v1.0.0-alpha"},
		{v: "v1.0.0+metadata"},
		{v: "v1.0.0-alpha+metadata"},
		{v: "v1.0.0-alpha.1"},
		{v: "v1.0.0-alpha.1+metadata"},
		{v: "v1.0.0-alpha.1.2"},
		{v: "v1.0.0-alpha.1.2+metadata"},
		{v: "v1.0.0-alpha.1.2.3"},
		{v: "v1.0.0-alpha.1.2.3+metadata"},
	} {
		t.Run(string(test.v), func(t *testing.T) {
			v, err := test.v.Parse()

			require.Equal(t, test.err, err != nil)
			if !test.err {
				require.Equal(t, uint64(1), v.Major())
				require.Equal(t, uint64(0), v.Minor())
				require.Equal(t, uint64(0), v.Patch())
			}
		})
	}
}
