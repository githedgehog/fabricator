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

alias k=kubectl

if command -v kubectl &>/dev/null; then
    source <(kubectl completion bash) 2>/dev/null || true
    complete -o default -F __start_kubectl k 2>/dev/null || true
fi
