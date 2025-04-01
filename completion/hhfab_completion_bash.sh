#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0


__hhfab_init_completion() {
  COMPREPLY=()
  _get_comp_words_by_ref "$@" cur prev words cword
}

__hhfab_bash_autocomplete() {
  if [[ "${COMP_WORDS[0]}" != "source" ]]; then
    local cur opts base words
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    if declare -F _init_completion >/dev/null 2>&1; then
      _init_completion -n "=:" || return
    else
      __hhfab_init_completion -n "=:" || return
    fi
    words=("${words[@]:0:$cword}")
    if [[ "$cur" == "-"* ]]; then
      requestComp="${words[*]} ${cur} --generate-shell-completion"
    else
      requestComp="${words[*]} --generate-shell-completion"
    fi
    opts=$(eval "${requestComp}" 2>/dev/null)
    COMPREPLY=($(compgen -W "${opts}" -- ${cur}))
    return 0
  fi
}

complete -o bashdefault -o default -o nospace -F __hhfab_bash_autocomplete hhfab
