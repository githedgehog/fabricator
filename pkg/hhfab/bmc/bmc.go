// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// Package bmc boots servers from an ISO over their BMC using Redfish virtual media
package bmc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

const (
	pollInterval      = 5 * time.Second
	powerStateTimeout = 2 * time.Minute
	requestTimeout    = 2 * time.Minute
)

var (
	ErrNoSystem       = errors.New("no computer system found")
	ErrNoCD           = errors.New("no virtual media slot supporting CD found")
	ErrNotInserted    = errors.New("virtual media isn't inserted after insert")
	ErrNotEjected     = errors.New("virtual media is still inserted after eject")
	ErrNoResetSupport = errors.New("neither ForceRestart nor PowerCycle reset is supported")
	ErrTaskFailed     = errors.New("task failed")
)

type Client struct {
	endpoint string
	api      *gofish.APIClient
	system   *schemas.ComputerSystem
}

func Connect(ctx context.Context, endpoint, username, password string) (*Client, error) {
	api, err := gofish.ConnectContext(ctx, gofish.ClientConfig{
		Endpoint:  endpoint,
		Username:  username,
		Password:  password,
		Insecure:  true,
		BasicAuth: true,
		// it's a new connection for each request otherwise
		ReuseConnections: true,
		// the context deadlines are only checked between the polls, so a hung BMC would block forever otherwise
		HTTPClient: &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: requestTimeout},
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", endpoint, err)
	}

	systems, err := api.GetService().Systems()
	if err != nil {
		api.Logout()

		return nil, fmt.Errorf("getting systems: %w", err)
	}
	if len(systems) == 0 {
		api.Logout()

		return nil, fmt.Errorf("%s: %w", endpoint, ErrNoSystem)
	}
	if len(systems) > 1 {
		slog.Warn("Multiple computer systems found, using the first one", "bmc", endpoint, "system", systems[0].ODataID)
	}

	return &Client{
		endpoint: endpoint,
		api:      api,
		system:   systems[0],
	}, nil
}

func (c *Client) Close() {
	c.api.Logout()
}

// virtualMedia returns the managers' virtual media slots, or the system's if they have none
func (c *Client) virtualMedia() ([]*schemas.VirtualMedia, error) {
	managers, err := c.api.GetService().Managers()
	if err != nil {
		return nil, fmt.Errorf("getting managers: %w", err)
	}

	media := []*schemas.VirtualMedia{}
	for _, manager := range managers {
		vms, err := manager.VirtualMedia()
		if err != nil {
			return nil, fmt.Errorf("getting manager %s virtual media: %w", manager.ID, err)
		}
		media = append(media, vms...)
	}

	if len(media) == 0 {
		vms, err := c.system.VirtualMedia()
		if err != nil {
			return nil, fmt.Errorf("getting system virtual media: %w", err)
		}
		media = vms
	}

	return media, nil
}

// waitTask polls the task monitor at a fixed interval, as some BMCs ask to retry every second
func (c *Client) waitTask(ctx context.Context, task *schemas.TaskMonitorInfo) error {
	if task == nil || task.TaskMonitor == "" {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for task %s: %w", task.TaskMonitor, ctx.Err())
		case <-time.After(pollInterval):
		}

		running, err := c.taskRunning(task.TaskMonitor)
		if err != nil {
			return fmt.Errorf("waiting for task %s: %w", task.TaskMonitor, err)
		}
		if !running {
			return nil
		}
	}
}

// taskRunning checks the task monitor, some BMCs answer with a failed task instead of an error status
func (c *Client) taskRunning(monitor string) (bool, error) {
	resp, err := c.api.Get(monitor)
	if err != nil {
		return false, err //nolint:wrapcheck
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		return true, nil
	}

	// a finished operation may return anything, not a task
	task := &schemas.Task{}
	if err := json.NewDecoder(resp.Body).Decode(task); err != nil {
		return false, nil
	}
	if slices.Contains(runningTaskStates, task.TaskState) {
		return true, nil
	}

	return false, taskError(task)
}

var runningTaskStates = []schemas.TaskState{
	schemas.NewTaskState,
	schemas.StartingTaskState,
	schemas.RunningTaskState,
	schemas.SuspendedTaskState,
	schemas.InterruptedTaskState,
	schemas.PendingTaskState,
	schemas.StoppingTaskState,
	schemas.CancellingTaskState,
}

// taskError returns an error with the task messages if the task failed
func taskError(task *schemas.Task) error {
	if !slices.Contains([]schemas.TaskState{schemas.ExceptionTaskState, schemas.KilledTaskState, schemas.CancelledTaskState}, task.TaskState) {
		return nil
	}

	msgs := []string{}
	for _, msg := range task.Messages {
		msgs = append(msgs, msg.Message)
	}

	return fmt.Errorf("%w: %s: %s", ErrTaskFailed, task.TaskState, strings.Join(msgs, "; "))
}

func isInserted(vm *schemas.VirtualMedia) bool {
	return vm.Inserted != nil && *vm.Inserted
}

func (c *Client) EjectAll(ctx context.Context) error {
	media, err := c.virtualMedia()
	if err != nil {
		return err
	}

	for _, vm := range media {
		if !isInserted(vm) {
			continue
		}

		slog.Debug("Ejecting virtual media", "bmc", c.endpoint, "slot", vm.ID, "image", vm.Image)

		task, err := vm.EjectMedia()
		if err != nil {
			return fmt.Errorf("ejecting virtual media %s: %w", vm.ID, err)
		}
		if err := c.waitTask(ctx, task); err != nil {
			return fmt.Errorf("ejecting virtual media %s: %w", vm.ID, err)
		}

		updated, err := schemas.GetVirtualMedia(c.api, vm.ODataID)
		if err != nil {
			return fmt.Errorf("getting virtual media %s: %w", vm.ID, err)
		}
		if isInserted(updated) {
			return fmt.Errorf("%s: %w", vm.ID, ErrNotEjected)
		}
	}

	return nil
}

// InsertCD inserts the image from the URL into the first CD slot, it should be empty
func (c *Client) InsertCD(ctx context.Context, url string) error {
	media, err := c.virtualMedia()
	if err != nil {
		return err
	}

	idx := slices.IndexFunc(media, func(vm *schemas.VirtualMedia) bool {
		return slices.Contains(vm.MediaTypes, schemas.CDVirtualMediaType)
	})
	if idx < 0 {
		return fmt.Errorf("%s: %w", c.endpoint, ErrNoCD)
	}
	vm := media[idx]

	slog.Debug("Inserting virtual media", "bmc", c.endpoint, "slot", vm.ID, "image", url)

	// AMI requires the transfer protocol
	task, err := vm.InsertMedia(&schemas.VirtualMediaInsertMediaParameters{
		Image:                url,
		Inserted:             new(true),
		WriteProtected:       new(true),
		TransferProtocolType: new(schemas.HTTPTransferProtocolType),
	})
	if err != nil {
		return fmt.Errorf("inserting virtual media %s: %w", vm.ID, err)
	}
	if err := c.waitTask(ctx, task); err != nil {
		return fmt.Errorf("inserting virtual media %s: %w", vm.ID, err)
	}

	updated, err := schemas.GetVirtualMedia(c.api, vm.ODataID)
	if err != nil {
		return fmt.Errorf("getting virtual media %s: %w", vm.ID, err)
	}
	if !isInserted(updated) {
		return fmt.Errorf("%s: %w", vm.ID, ErrNotInserted)
	}

	return nil
}

func (c *Client) BootOnceFromCD() error {
	if err := c.system.SetBoot(&schemas.Boot{
		BootSourceOverrideEnabled: schemas.OnceBootSourceOverrideEnabled,
		BootSourceOverrideTarget:  schemas.CdBootSource,
		BootSourceOverrideMode:    schemas.UEFIBootSourceOverrideMode,
	}); err != nil {
		return fmt.Errorf("setting one-time boot from CD: %w", err)
	}

	return nil
}

// Reset restarts the system or powers it on if off
func (c *Client) Reset(ctx context.Context) error {
	system, err := c.refreshSystem()
	if err != nil {
		return err
	}

	resetType := schemas.OnResetType
	if system.PowerState != schemas.OffPowerState {
		supported, err := system.GetSupportedResetTypes()
		if err != nil {
			return fmt.Errorf("getting supported reset types: %w", err)
		}

		switch {
		case len(supported) == 0 || slices.Contains(supported, schemas.ForceRestartResetType):
			resetType = schemas.ForceRestartResetType
		case slices.Contains(supported, schemas.PowerCycleResetType):
			resetType = schemas.PowerCycleResetType
		default:
			return fmt.Errorf("%s: %w", c.endpoint, ErrNoResetSupport)
		}
	}

	return c.reset(ctx, system, resetType)
}

func (c *Client) PowerOn(ctx context.Context) error {
	system, err := c.refreshSystem()
	if err != nil {
		return err
	}
	if system.PowerState == schemas.OnPowerState {
		slog.Debug("System is already on", "bmc", c.endpoint)

		return nil
	}

	if err := c.reset(ctx, system, schemas.OnResetType); err != nil {
		return err
	}

	return c.waitPowerState(ctx, schemas.OnPowerState)
}

func (c *Client) PowerOff(ctx context.Context) error {
	system, err := c.refreshSystem()
	if err != nil {
		return err
	}
	if system.PowerState == schemas.OffPowerState {
		slog.Debug("System is already off", "bmc", c.endpoint)

		return nil
	}

	if err := c.reset(ctx, system, schemas.ForceOffResetType); err != nil {
		return err
	}

	return c.waitPowerState(ctx, schemas.OffPowerState)
}

// waitPowerState waits for the system to report the state, the reset task may finish before it does
func (c *Client) waitPowerState(ctx context.Context, state schemas.PowerState) error {
	ctx, cancel := context.WithTimeout(ctx, powerStateTimeout)
	defer cancel()

	for {
		system, err := c.refreshSystem()
		if err != nil {
			slog.Debug("Failed to get system while waiting for power state", "bmc", c.endpoint, "err", err)
		} else if system.PowerState == state {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for power state %s: %w", state, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func (c *Client) refreshSystem() (*schemas.ComputerSystem, error) {
	system, err := schemas.GetComputerSystem(c.api, c.system.ODataID)
	if err != nil {
		return nil, fmt.Errorf("getting system: %w", err)
	}
	c.system = system

	return system, nil
}

func (c *Client) reset(ctx context.Context, system *schemas.ComputerSystem, resetType schemas.ResetType) error {
	slog.Debug("Resetting system", "bmc", c.endpoint, "type", resetType, "power", system.PowerState)

	task, err := system.Reset(resetType)
	if err != nil {
		return fmt.Errorf("resetting system (%s): %w", resetType, err)
	}
	if err := c.waitTask(ctx, task); err != nil {
		return fmt.Errorf("resetting system (%s): %w", resetType, err)
	}

	return nil
}

func (c *Client) WaitBootOverrideConsumed(ctx context.Context) error {
	for {
		system, err := schemas.GetComputerSystem(c.api, c.system.ODataID)
		if err != nil {
			slog.Debug("Failed to get system while waiting for boot override", "bmc", c.endpoint, "err", err)
		} else if system.Boot.BootSourceOverrideEnabled != schemas.OnceBootSourceOverrideEnabled {
			c.system = system

			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for boot override to be consumed: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}
