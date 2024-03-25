// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vlab

import (
	"context"
	"log/slog"
	"strings"

	"github.com/pkg/errors"
	"github.com/shirou/gopsutil/v3/process"
)

func checkForStaleVMs(ctx context.Context, killStaleVMs bool) error {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return errors.Wrap(err, "error getting processes")
	}

	stale := []int32{}
	for _, pr := range processes {
		cmd, err := pr.CmdlineSliceWithContext(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "no such file or directory") {
				continue
			}

			return errors.Wrap(err, "error getting process cmdline")
		}

		// only one instance of VLAB supported at the same time
		if len(cmd) < 6 || cmd[0] != "qemu-system-x86_64" || cmd[1] != "-name" || cmd[3] != "-uuid" {
			continue
		}
		if !strings.HasPrefix(cmd[4], "00000000-0000-0000-0000-0000000000") {
			continue
		}

		if killStaleVMs {
			slog.Warn("Found stale VM process, killing it", "pid", pr.Pid)
			err = pr.KillWithContext(ctx)
			if err != nil {
				return errors.Wrapf(err, "error killing stale VM process %d", pr.Pid)
			}
		} else {
			slog.Error("Found stale VM process", "pid", pr.Pid)
			stale = append(stale, pr.Pid)
			// return errors.Errorf("found stale VM process %d, kill it and try again or run vlab with --kill-stale-vms", pr.Pid)
		}
	}

	if len(stale) > 0 {
		return errors.Errorf("found stale VM processes %v, kill them and try again or run vlab with --kill-stale-vms", stale)
	}

	return nil
}
