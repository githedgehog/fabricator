// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"encoding/binary"
	"iter"
	"net/netip"

	"go.githedgehog.com/fabric/api/meta"
)

func VLANsFrom(ranges ...meta.VLANRange) iter.Seq[uint16] {
	return func(yield func(uint16) bool) {
		for _, vlanRange := range ranges {
			for vlan := vlanRange.From; vlan <= vlanRange.To; vlan++ {
				if !yield(vlan) {
					return
				}
			}
		}
	}
}

func AddrsFrom(prefixes ...netip.Prefix) iter.Seq[netip.Prefix] {
	return func(yield func(netip.Prefix) bool) {
		for _, prefix := range prefixes {
			for addr := prefix.Masked().Addr(); addr.IsValid() && prefix.Contains(addr); addr = addr.Next() {
				if !yield(netip.PrefixFrom(addr, prefix.Bits())) {
					return
				}
			}
		}
	}
}

func SubPrefixesFrom(bits int, prefixes ...netip.Prefix) iter.Seq[netip.Prefix] {
	return func(yield func(netip.Prefix) bool) {
		for _, prefix := range prefixes {
			if bits < prefix.Bits() || !prefix.Addr().Is4() {
				continue
			}

			addr := prefix.Masked().Addr()
			addrBytes := addr.AsSlice()
			addrUint := binary.BigEndian.Uint32(addrBytes)
			ok := true

			for ok && prefix.Contains(addr) {
				if !yield(netip.PrefixFrom(addr, bits)) {
					return
				}

				addrUint += 1 << uint(32-bits)
				binary.BigEndian.PutUint32(addrBytes, addrUint)
				addr, ok = netip.AddrFromSlice(addrBytes)
			}
		}
	}
}

func CollectN[E any](n int, seq iter.Seq[E]) []E {
	res := make([]E, n)

	idx := 0
	for v := range seq {
		if idx >= n {
			break
		}

		res[idx] = v
		idx++
	}

	if idx == 0 {
		return nil
	}

	return res[:idx:idx]
}
