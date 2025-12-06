// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package timeline

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Timeline provides a simple event logger that records events with elapsed time.
// Events are written to a file in a human-readable format showing when each event
// occurred relative to the start of the timeline.
type Timeline struct {
	file  *os.File
	mu    sync.Mutex
	start time.Time
}

// New creates a new Timeline that writes to the specified file path.
// The timeline starts immediately and writes a header to the file.
func New(filepath string) (*Timeline, error) {
	f, err := os.Create(filepath)
	if err != nil {
		return nil, fmt.Errorf("creating timeline file: %w", err)
	}

	start := time.Now()

	t := &Timeline{
		file:  f,
		start: start,
	}

	// Write header
	fmt.Fprintf(f, "VLAB EXECUTION TIMELINE\n")
	fmt.Fprintf(f, "=======================\n")
	fmt.Fprintf(f, "Started: %s\n\n", start.Format(time.RFC3339))

	return t, nil
}

// Log records an event to the timeline with the current elapsed time.
// This method is thread-safe.
func (t *Timeline) Log(event string) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	elapsed := time.Since(t.start)
	timestamp := fmt.Sprintf("+%02d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)

	fmt.Fprintf(t.file, "%s %s\n", timestamp, event)
	t.file.Sync() //nolint:errcheck
}

// Logf records a formatted event to the timeline.
// This method is thread-safe.
func (t *Timeline) Logf(format string, args ...interface{}) {
	t.Log(fmt.Sprintf(format, args...))
}

// Close closes the timeline file.
func (t *Timeline) Close() error {
	if t == nil || t.file == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.file.Close(); err != nil {
		return fmt.Errorf("closing timeline file: %w", err)
	}

	return nil
}
