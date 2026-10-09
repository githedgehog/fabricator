// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package dhcpload

import (
	"context"
	"errors"
)

var errAccessUnsupported = errors.New("access mode needs raw packet sockets and is only supported on Linux")

func (c *Config) openAccessEndpoints(_ context.Context, _ *stats) ([]endpoint, error) {
	return nil, errAccessUnsupported
}
