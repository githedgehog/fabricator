// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bmc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
	"github.com/stretchr/testify/require"
)

func TestTaskRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redfish/v1/":
			_, _ = w.Write([]byte(`{"@odata.id": "/redfish/v1/"}`))
		case "/tasks/running":
			w.WriteHeader(http.StatusAccepted)
		case "/tasks/done":
			_, _ = w.Write([]byte(`{"TaskState": "Completed"}`))
		case "/tasks/gone":
			w.WriteHeader(http.StatusGone)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	api, err := gofish.ConnectContext(t.Context(), gofish.ClientConfig{Endpoint: srv.URL})
	require.NoError(t, err)
	c := &Client{endpoint: srv.URL, api: api}

	running, err := c.taskRunning("/tasks/running")
	require.NoError(t, err)
	require.True(t, running)

	// the monitor may be removed once the task is done
	for _, monitor := range []string{"/tasks/done", "/tasks/gone"} {
		running, err := c.taskRunning(monitor)
		require.NoError(t, err, monitor)
		require.False(t, running, monitor)
	}

	_, err = c.taskRunning("/tasks/error")
	require.Error(t, err)
}

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
