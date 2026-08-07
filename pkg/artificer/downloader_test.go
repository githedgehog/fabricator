// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package artificer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDownloaderLookupCache(t *testing.T) {
	primary := t.TempDir()
	extra1 := t.TempDir()
	extra2 := t.TempDir()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(os.MkdirAll(filepath.Join(primary, "in-primary.oras"), 0o700))
	must(os.MkdirAll(filepath.Join(extra1, "in-primary.oras"), 0o700))
	must(os.MkdirAll(filepath.Join(extra2, "in-extra2.oci"), 0o700))
	must(os.WriteFile(filepath.Join(extra1, "not-a-dir.oras"), []byte("x"), 0o600))

	d := &Downloader{cacheDir: primary, extraCacheDirs: []string{extra1, extra2}}

	for _, tc := range []struct {
		name string
		want string
	}{
		{"in-primary.oras", filepath.Join(primary, "in-primary.oras")}, // primary wins over extra1
		{"in-extra2.oci", filepath.Join(extra2, "in-extra2.oci")},      // found in second extra
		{"not-a-dir.oras", ""}, // non-dir in extra is skipped
		{"missing.oras", ""},   // nowhere
	} {
		got, err := d.lookupCache(tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	// non-dir in primary is an error
	must(os.WriteFile(filepath.Join(primary, "bad.oras"), []byte("x"), 0o600))
	if _, err := d.lookupCache("bad.oras"); err == nil {
		t.Error("expected error for non-dir primary cache entry")
	}
}
