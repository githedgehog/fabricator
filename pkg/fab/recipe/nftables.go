// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"

	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
)

// nftablesRulesFilePath and nftUnitFilePath are the two files written on
// control nodes. They are the single source of truth for these paths: the
// butane templates (fresh install) hardcode the same strings and the upgrade
// path writes them here, so the install and upgrade paths can never drift apart.
// nftServiceName must match the unit name declared in the butane templates.
const (
	nftablesRulesFilePath = "/etc/hh/nftables.conf"
	nftUnitFilePath       = "/etc/systemd/system/hh-nftables.service"
	nftServiceName        = "hh-nftables.service"
)

const nftablesTmpl = `#Managed by Hedgehog, do not edit
destroy table inet HH-ext-intf-fw
table inet HH-ext-intf-fw {
    counter ext_dropped {
        comment "unexpected inbound connections dropped on external interface"
    }

    chain input {
        type filter hook input priority filter - 5;
        policy accept;
        # Always allow return traffic for connections this host initiated
        iifname "{{ .ExtInterface }}" ct state established,related accept
        iifname "{{ .ExtInterface }}" icmp type echo-request accept
        iifname "{{ .ExtInterface }}" tcp dport { 22, 6443 } accept
        iifname "{{ .ExtInterface }}" counter name ext_dropped drop
    }
}`

const nftSystemdUnitTmpl = `[Unit]
Description=Hedgehog rules for external interface, scoped only to HH tables
Documentation=man:nft(8)
Wants=network-pre.target
Before=network-pre.target shutdown.target
Conflicts=shutdown.target
DefaultDependencies=no

[Service]
Type=oneshot
RemainAfterExit=yes
ProtectSystem=full
ProtectHome=true
ExecStart=/usr/bin/nft -f {{ .RulesPath }}
ExecReload=/usr/bin/nft -f {{ .RulesPath }}
ExecStop=/usr/bin/nft destroy table inet HH-ext-intf-fw

[Install]
WantedBy=multi-user.target`

func renderNftablesRules(intf string) (string, error) {
	out, err := tmplutil.FromTemplate("nftables-config", nftablesTmpl, map[string]any{
		"ExtInterface": intf,
	})
	if err != nil {
		return "", fmt.Errorf("rendering nftables config: %w", err)
	}

	return out, nil
}

func renderNftablesSystemdUnit() (string, error) {
	out, err := tmplutil.FromTemplate("nftables-service", nftSystemdUnitTmpl, map[string]any{
		"RulesPath": nftablesRulesFilePath,
	})
	if err != nil {
		return "", fmt.Errorf("rendering nftables service unit: %w", err)
	}

	return out, nil
}
