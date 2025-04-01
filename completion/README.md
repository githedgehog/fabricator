# Bash Completion for `hhfab`

This directory contains support for Bash autocompletion for the `hhfab` CLI.

## Installation

### 1. Install the Completion Script

Copy the existing completion script to the system completion directory:

```bash
sudo cp hhfab_completion_bash.sh /etc/bash_completion.d/hhfab
sudo chmod +r /etc/bash_completion.d/hhfab
```

### 2. Reload Shell Completion

Apply the changes in your current shell:

```bash
source /etc/bash_completion.d/hhfab
```

Or restart your terminal session.

### 3. Test Completion

Top-level command completion:

```bash
hhfab <TAB><TAB>
```

Should produce something like:

```
build     diagram   help      init      validate  versions  vlab
```

Subcommand flag completion:

```bash
hhfab vlab <SPACE><TAB><TAB>
```

Currently, only command names are supported.

**Note:** Flag and argument completion is not yet supported in `urfave/cli/v3`.

## Using with `just`

You can install the completion script using the `completion` target in the `justfile`:

```bash
just completion
```

This will:

- Copy `completion/hhfab_completion.sh` to `/etc/bash_completion.d/hhfab`
- Ensure the file is readable
- Reload the script in your current shell (if supported)
