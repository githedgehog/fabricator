package cnc

import (
	"log/slog"
	"time"

	"github.com/pkg/errors"
)

type WaitParams struct {
	Delay    time.Duration `json:"delay,omitempty"`
	Interval time.Duration `json:"interval,omitempty"`
	Attempts int           `json:"attempts,omitempty"`
}

func (w *WaitParams) Hydrate() error {
	if w.Interval == 0 {
		w.Interval = 1 * time.Second
	}
	if w.Attempts <= 0 {
		return errors.New("attempts should be positive number")
	}

	return nil
}

func (w *WaitParams) Wait(checker func() error) error {
	time.Sleep(w.Delay)

	var err error
	for attempt := 0; attempt < w.Attempts; attempt += 1 {
		err = checker()
		if err != nil {
			slog.Debug("Attempt failed", "idx", attempt, "max", w.Attempts, "err", err)
		} else {
			slog.Debug("Attempt success", "idx", attempt, "max", w.Attempts)
			break
		}

		time.Sleep(w.Interval)
	}

	// TODO maybe slog?
	return errors.Wrapf(err, "failed after %d attempts", w.Attempts)
}
