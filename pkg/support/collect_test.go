// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilexec "k8s.io/client-go/util/exec"
)

func TestRetriable(t *testing.T) {
	gr := schema.GroupResource{Group: "vpc.githedgehog.com", Resource: "vpcs"}

	for name, tc := range map[string]struct {
		err       error
		retriable bool
	}{
		"forbidden":            {fmt.Errorf("listing: %w", kapierrors.NewForbidden(gr, "", errors.New("no access"))), false}, //nolint:err113 // test error
		"unauthorized":         {kapierrors.NewUnauthorized("no token"), false},
		"invalid":              {kapierrors.NewInvalid(schema.GroupKind{Kind: "VPC"}, "vpc-1", nil), false},
		"bad request":          {kapierrors.NewBadRequest("container is waiting to start"), false},
		"not found":            {kapierrors.NewNotFound(gr, "vpc-1"), false},
		"method not supported": {kapierrors.NewMethodNotSupported(gr, "list"), false},
		"command failed":       {fmt.Errorf("executing: %w", utilexec.CodeExitError{Err: errors.New("exit 1"), Code: 1}), false}, //nolint:err113 // test error
		"unavailable":          {kapierrors.NewServiceUnavailable("busy"), true},
		"timeout":              {kapierrors.NewTimeoutError("slow", 1), true},
		"too many requests":    {kapierrors.NewTooManyRequests("throttled", 1), true},
		"connection refused":   {errors.New("dial tcp 172.30.0.1:6443: connect: connection refused"), true}, //nolint:err113 // test error
	} {
		require.Equal(t, tc.retriable, retriable(t.Context())(tc.err), name)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.False(t, retriable(ctx)(kapierrors.NewServiceUnavailable("busy")), "nothing is retried once the context is done")
}
