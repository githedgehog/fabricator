#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0
export LC_ALL=C
export LANG=C

if [ -f /opt/bash-completion/bash_completion ]; then
    . /opt/bash-completion/bash_completion
else
    echo "Warning: bash_completion file not found" >&2
fi

HEDGEHOG_COLOR='\e[33m'    # Yellow
RESET_COLOR='\e[0m'

alias k=kubectl

if command -v kubectl &>/dev/null && [ -f /opt/bash-completion/bash_completion ]; then
    source <(kubectl completion bash) 2>/dev/null || true
    complete -o default -F __start_kubectl kubectl 2>/dev/null || true
    complete -o default -F __start_kubectl k 2>/dev/null || true
fi

# Hedgehog resources list helper
hhres() {
    echo -e "${HEDGEHOG_COLOR}🦔 Hedgehog Resources:${RESET_COLOR}"
    kubectl api-resources | grep -E "SHORTNAMES|githedgehog.com" | sort | while read -r line; do
        if [[ "$line" =~ ^NAME ]]; then
            echo -e "${HEDGEHOG_COLOR}${line}${RESET_COLOR}"
        else
            echo "$line"
        fi
    done
}
