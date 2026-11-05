// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOSRelease(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		want    map[string]string
	}{
		{
			name: "flatcar-unquoted",
			content: `NAME="Flatcar Container Linux by Kinvolk"
ID=flatcar
ID_LIKE=coreos
VERSION=4230.2.3
VERSION_ID=4230.2.3
PRETTY_NAME="Flatcar Container Linux by Kinvolk 4230.2.3 (Oklo)"
`,
			want: map[string]string{
				"NAME":        "Flatcar Container Linux by Kinvolk",
				"ID":          "flatcar",
				"ID_LIKE":     "coreos",
				"VERSION":     "4230.2.3",
				"VERSION_ID":  "4230.2.3",
				"PRETTY_NAME": "Flatcar Container Linux by Kinvolk 4230.2.3 (Oklo)",
			},
		},
		{
			name: "flatcar-quoted",
			content: `NAME="Flatcar Container Linux by Kinvolk"
ID="flatcar"
ID_LIKE="coreos"
VERSION="4757.2.3"
VERSION_ID="4757.2.3"
BUILD_ID="2026-10-27-1008"
SYSEXT_LEVEL="1.0"
PRETTY_NAME="Flatcar Container Linux by Kinvolk 4757.2.3"
ANSI_COLOR="38;5;75"
HOME_URL="https://flatcar.org"
BUG_REPORT_URL="https://issues.flatcar.org"
SUPPORT_URL="https://groups.google.com/forum/#!forum/flatcar-linux-user"
FLATCAR_BOARD="amd64-usr"
CPE_NAME="cpe:2.3:o:flatcar-linux:flatcar_linux:4757.2.3:*:*:*:*:*:*:*"
`,
			want: map[string]string{
				"NAME":           "Flatcar Container Linux by Kinvolk",
				"ID":             "flatcar",
				"ID_LIKE":        "coreos",
				"VERSION":        "4757.2.3",
				"VERSION_ID":     "4757.2.3",
				"BUILD_ID":       "2026-10-27-1008",
				"SYSEXT_LEVEL":   "1.0",
				"PRETTY_NAME":    "Flatcar Container Linux by Kinvolk 4757.2.3",
				"ANSI_COLOR":     "38;5;75",
				"HOME_URL":       "https://flatcar.org",
				"BUG_REPORT_URL": "https://issues.flatcar.org",
				"SUPPORT_URL":    "https://groups.google.com/forum/#!forum/flatcar-linux-user",
				"FLATCAR_BOARD":  "amd64-usr",
				"CPE_NAME":       "cpe:2.3:o:flatcar-linux:flatcar_linux:4757.2.3:*:*:*:*:*:*:*",
			},
		},
		{
			name: "ubuntu",
			content: `PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.1 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
`,
			want: map[string]string{
				"PRETTY_NAME":      "Ubuntu 24.04.1 LTS",
				"NAME":             "Ubuntu",
				"VERSION_ID":       "24.04",
				"VERSION":          "24.04.1 LTS (Noble Numbat)",
				"VERSION_CODENAME": "noble",
				"ID":               "ubuntu",
				"ID_LIKE":          "debian",
			},
		},
		{
			name:    "empty",
			content: "",
			want:    map[string]string{},
		},
		{
			name:    "comments-and-blank-lines",
			content: "# comment\n\n   \n\t\n  # indented comment\nID=flatcar\n#VERSION_ID=1\n",
			want:    map[string]string{"ID": "flatcar"},
		},
		{
			name:    "surrounding-whitespace",
			content: "  ID=flatcar  \n\tVERSION_ID=1\t\n",
			want:    map[string]string{"ID": "flatcar", "VERSION_ID": "1"},
		},
		{
			name:    "crlf-and-no-trailing-newline",
			content: "ID=\"flatcar\"\r\nVERSION_ID=1",
			want:    map[string]string{"ID": "flatcar", "VERSION_ID": "1"},
		},
		{
			name:    "double-quoted-keeps-inner-whitespace",
			content: `NAME="  a  b  "`,
			want:    map[string]string{"NAME": "  a  b  "},
		},
		{
			name:    "single-quoted",
			content: "ID='flatcar'\nNAME='  a  b  '",
			want:    map[string]string{"ID": "flatcar", "NAME": "  a  b  "},
		},
		{
			name:    "double-quoted-escapes",
			content: `X="a \"b\" \$c \\d \` + "`e`\"",
			want:    map[string]string{"X": "a \"b\" $c \\d `e`"},
		},
		{
			name:    "single-quoted-no-escapes",
			content: `X='a \"b\" \$c'`,
			want:    map[string]string{"X": `a \"b\" \$c`},
		},
		{
			name:    "mismatched-quotes-kept",
			content: "A=\"abc'\nB='abc\"\nC=\"abc\nD=\"",
			want:    map[string]string{"A": `"abc'`, "B": `'abc"`, "C": `"abc`, "D": `"`},
		},
		{
			name:    "empty-values",
			content: "A=\nB=\"\"\nC=''",
			want:    map[string]string{"A": "", "B": "", "C": ""},
		},
		{
			name:    "equals-in-value",
			content: "A=\"a=b\"\nB=c=d",
			want:    map[string]string{"A": "a=b", "B": "c=d"},
		},
		{
			name:    "invalid-lines-skipped",
			content: "NOEQ\n=novalue\nKEY = value\nKEY\t=value\nID=flatcar",
			want:    map[string]string{"ID": "flatcar"},
		},
		{
			name:    "last-wins",
			content: "ID=ubuntu\nID=flatcar",
			want:    map[string]string{"ID": "flatcar"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseOSRelease(tt.content))
		})
	}
}
