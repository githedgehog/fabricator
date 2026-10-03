// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUdevProperty(t *testing.T) {
	t.Parallel()

	// as `udevadm info --query=property` prints it for a virtio NIC Flatcar
	// named eth0
	props := []byte(`DEVPATH=/devices/pci0000:00/0000:00:1e.0/0000:01:01.0/0000:02:01.0/virtio2/net/eth0
SUBSYSTEM=net
INTERFACE=eth0
ID_NET_NAMING_SCHEME=v260
ID_NET_NAME_MAC=enx0c2012fe0001
ID_NET_NAME_PATH=enp2s1
ID_NET_DRIVER=virtio_net
ID_NET_LINK_FILE=/usr/lib/systemd/network/98-virtio.link
ID_NET_NAME=eth0
`)

	require.Equal(t, "enp2s1", udevProperty(props, "ID_NET_NAME_PATH"))
	require.Equal(t, "eth0", udevProperty(props, "ID_NET_NAME"))
	require.Empty(t, udevProperty(props, "ID_NET_NAME_SLOT"))
	require.Empty(t, udevProperty(nil, "ID_NET_NAME_PATH"))
}

// A target name some interface already has is refused before anything is
// touched, rather than failing halfway with the interface down.
func TestRenameIfaceRefusesTakenName(t *testing.T) {
	t.Parallel()

	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	require.NotEmpty(t, ifaces)

	err = renameIface(t.Context(), "does-not-exist", ifaces[0].Name)
	require.ErrorContains(t, err, "another interface is already named "+ifaces[0].Name)
}
