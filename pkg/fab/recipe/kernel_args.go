// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"go.githedgehog.com/fabric/pkg/util/logutil"
)

const (
	oemGrubCfg        = "/oem/grub.cfg"
	procCmdline       = "/proc/cmdline"
	procBootID        = "/proc/sys/kernel/random/boot_id"
	linuxAppendPrefix = `set linux_append="`
)

// ensureKernelArgs sets the args in the grub config the same way as the ignition kargs helper and fails unless the
// kernel is already booted with them, VLAB VMs are rebooted once to get them applied
func ensureKernelArgs(ctx context.Context, keys, args []string) error {
	cfg, err := os.ReadFile(oemGrubCfg)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %q: %w", oemGrubCfg, err)
	}

	if updated := setKernelArgs(cfg, keys, args); !bytes.Equal(updated, cfg) {
		slog.Info("Updating kernel args", "path", oemGrubCfg, "args", args)

		if err := os.WriteFile(oemGrubCfg, updated, 0o644); err != nil { //nolint:gosec
			return fmt.Errorf("writing %q: %w", oemGrubCfg, err)
		}
	}

	cmdline, err := os.ReadFile(procCmdline)
	if err != nil {
		return fmt.Errorf("reading %q: %w", procCmdline, err)
	}

	want := strings.Join(args, " ")
	if kernelArgsApplied(string(cmdline), keys, args) {
		if err := os.Remove(RebootMarkerFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing reboot marker: %w", err)
		}

		return nil
	}

	if !isVLAB() {
		return fmt.Errorf("kernel args %q aren't applied, reboot the node and re-run the installer", want) //nolint:err113
	}

	bootID, err := os.ReadFile(procBootID)
	if err != nil {
		return fmt.Errorf("reading boot id: %w", err)
	}

	// the marker is the boot id and the args the reboot was for, so it's only failing if it's actually rebooted
	marker := strings.TrimSpace(string(bootID)) + " " + want
	prev, err := os.ReadFile(RebootMarkerFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading reboot marker: %w", err)
	}
	if prevBootID, prevArgs, _ := strings.Cut(string(prev), " "); err == nil && prevArgs == want && prevBootID != strings.TrimSpace(string(bootID)) {
		return fmt.Errorf("kernel args %q aren't applied even after a reboot", want) //nolint:err113
	}

	if err := os.MkdirAll(HedgehogDir, 0o755); err != nil {
		return fmt.Errorf("creating hedgehog dir %q: %w", HedgehogDir, err)
	}
	if err := os.WriteFile(RebootMarkerFile, []byte(marker), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing reboot marker: %w", err)
	}

	slog.Warn("Rebooting VLAB VM to apply kernel args", "args", want)

	cmd := exec.CommandContext(ctx, "systemctl", "reboot")
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "systemctl: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "systemctl: ")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rebooting: %w", err)
	}

	return fmt.Errorf("rebooting to apply kernel args %q", want) //nolint:err113
}

// setKernelArgs merges all unconditional linux_append lines into a single one, replacing all args with the keys by the
// args, the same format the ignition kargs helper uses so it works with both
func setKernelArgs(cfg []byte, keys, args []string) []byte {
	out := &bytes.Buffer{}
	opts := []string{}
	found := false

	for line := range strings.Lines(string(cfg)) {
		value, ok := strings.CutPrefix(line, linuxAppendPrefix)
		if !ok {
			out.WriteString(line)

			continue
		}

		found = true
		value, _, _ = strings.Cut(value, `"`)
		for opt := range strings.FieldsSeq(strings.ReplaceAll(value, "$linux_append", "")) {
			if key, _, _ := strings.Cut(opt, "="); !slices.Contains(keys, key) {
				opts = append(opts, opt)
			}
		}
	}

	opts = append(opts, args...)
	if !found && len(args) == 0 {
		return cfg
	}

	if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		out.WriteString("\n")
	}
	if len(opts) > 0 {
		fmt.Fprintf(out, "%s$linux_append %s\"\n", linuxAppendPrefix, strings.Join(opts, " "))
	}

	return out.Bytes()
}

// kernelArgsApplied checks that the args with the keys in the cmdline are exactly the args, in the same order
func kernelArgsApplied(cmdline string, keys, args []string) bool {
	applied := []string{}
	for opt := range strings.FieldsSeq(cmdline) {
		if key, _, _ := strings.Cut(opt, "="); slices.Contains(keys, key) {
			applied = append(applied, opt)
		}
	}

	return slices.Equal(applied, args)
}

// checkHugePages only checks the hugepages as they're reserved on boot by the kernel args, allocating them later
// wouldn't help as memory only gets more fragmented
func checkHugePages(sizeKB, want int) error {
	if want == 0 {
		return nil
	}

	raw, err := os.ReadFile(fmt.Sprintf("/sys/kernel/mm/hugepages/hugepages-%dkB/nr_hugepages", sizeKB))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%dkB hugepages aren't supported", sizeKB) //nolint:err113
	}
	if err != nil {
		return fmt.Errorf("reading %dkB hugepages: %w", sizeKB, err)
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("parsing %dkB hugepages: %w", sizeKB, err)
	}

	if got < want {
		return fmt.Errorf("%dkB hugepages reserved on boot: want %d, got %d, check memory and dmesg for HugeTLB", sizeKB, want, got) //nolint:err113
	}

	slog.Info("Hugepages reserved", "sizeKB", sizeKB, "count", got)

	return nil
}
