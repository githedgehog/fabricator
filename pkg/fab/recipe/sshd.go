// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"

	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
)

// sshdConfigPath is the sshd drop-in written on both control and fab nodes.
const sshdConfigPath = "/etc/ssh/sshd_config.d/40-hedgehog.conf"

// sshdConfigTmpl is the single source of truth for the contents of 40-hedgehog.conf.
// It is rendered for both fresh installs (injected into the butane templates) and
// upgrades (written to disk directly), so the two paths can never drift apart.
// Keep the closing backtick on the MACs line so the body has no trailing newline.
const sshdConfigTmpl = `# Customized by Hedgehog
PerSourceMaxStartups 4
PerSourcePenalties crash:5m authfail:2m
LoginGraceTime 20
PermitRootLogin no
MaxAuthTries 3
MaxSessions 5
PasswordAuthentication {{ .PasswordAuth }}
KbdInteractiveAuthentication no
Compression no
ClientAliveInterval 300
ClientAliveCountMax 2
MaxStartups 10:30:60
KexAlgorithms sntrup761x25519-sha512@openssh.com,curve25519-sha256,curve25519-sha256@libssh.org
Ciphers chacha20-poly1305@openssh.com,aes256-gcm@openssh.com,aes128-gcm@openssh.com
MACs hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com,umac-128-etm@openssh.com`

// renderSSHDConfig produces the 40-hedgehog.conf body with password auth toggled.
func renderSSHDConfig(noPassAuth bool) (string, error) {
	passwordAuth := "yes"
	if noPassAuth {
		passwordAuth = "no"
	}

	out, err := tmplutil.FromTemplate("sshd-config", sshdConfigTmpl, map[string]any{
		"PasswordAuth": passwordAuth,
	})
	if err != nil {
		return "", fmt.Errorf("rendering sshd config: %w", err)
	}

	return out, nil
}
