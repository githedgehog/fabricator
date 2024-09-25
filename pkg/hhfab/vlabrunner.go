package hhfab

import (
	"context"
	"fmt"
)

func (c *Config) VLABRun(ctx context.Context, vlab *VLAB, killStale bool) error {
	stale, err := CheckStaleVMs(ctx, killStale)
	if err != nil {
		return fmt.Errorf("checking for stale VMs: %w", err)
	}
	if len(stale) > 0 && killStale {
		return fmt.Errorf("%d stale VM(s) found: rerun with --kill-stale to autocleanup", len(stale))
	}

	return nil
}
