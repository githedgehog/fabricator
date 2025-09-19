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

# Colors
HEDGEHOG_COLOR='\e[33m'    # Yellow
RESET_COLOR='\e[0m'

alias k=kubectl

# Load kubectl completion only if bash-completion is available
if command -v kubectl &>/dev/null && [ -f /opt/bash-completion/bash_completion ]; then
    source <(kubectl completion bash) 2>/dev/null || true
    complete -o default -F __start_kubectl kubectl 2>/dev/null || true
    complete -o default -F __start_kubectl k 2>/dev/null || true
fi

# Show Hedgehog resources only
hres() {
    echo -e "${HEDGEHOG_COLOR}🦔 Hedgehog Resources:${RESET_COLOR}"
    kubectl api-resources --no-headers | grep "githedgehog.com" | sort
}

echo -e "${HEDGEHOG_COLOR}🦔 Hedgehog kubectl enhancements loaded!${RESET_COLOR}"
echo "Available commands:"
if [ -f /opt/bash-completion/bash_completion ]; then
    echo -e "  ${HEDGEHOG_COLOR}k${RESET_COLOR}          - kubectl alias (TAB to autocomplete)"
else
    echo -e "  ${HEDGEHOG_COLOR}k${RESET_COLOR}          - kubectl alias"
fi
echo -e "  ${HEDGEHOG_COLOR}hres${RESET_COLOR}       - show Hedgehog resources only"
