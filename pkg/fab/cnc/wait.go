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
	// TODO Timeout?
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
