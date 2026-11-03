// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bmc

import (
	"testing"

	"github.com/stmcginnis/gofish/schemas"
	"github.com/stretchr/testify/require"
)

func TestTaskError(t *testing.T) {
	require.NoError(t, taskError(&schemas.Task{}))
	require.NoError(t, taskError(&schemas.Task{TaskState: schemas.CompletedTaskState}))

	err := taskError(&schemas.Task{
		TaskState: schemas.ExceptionTaskState,
		Messages:  []schemas.Message{{Message: "power action failed"}},
	})
	require.ErrorIs(t, err, ErrTaskFailed)
	require.ErrorContains(t, err, "Exception: power action failed")

	require.ErrorIs(t, taskError(&schemas.Task{TaskState: schemas.KilledTaskState}), ErrTaskFailed)
}
