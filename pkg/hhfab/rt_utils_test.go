// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVPCAttachmentName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "server-1--unbundled--leaf-01--vpc-01--subnet-01", vpcAttachmentName("server-1--unbundled--leaf-01", "vpc-01", "subnet-01"))

	long := vpcAttachmentName("server-5--eslag--ds3000-03--sse-c4632-01", "regular-01", "subnet-01")
	require.LessOrEqual(t, len(long), maxResourceNameLen)
	require.Equal(t, long, vpcAttachmentName("server-5--eslag--ds3000-03--sse-c4632-01", "regular-01", "subnet-01"))
	require.NotEqual(t, long, vpcAttachmentName("server-5--eslag--ds3000-03--sse-c4632-01", "regular-02", "subnet-01"))
}
