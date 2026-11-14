// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var (
	errMasqPoolNotFound   = errors.New("no TCP masquerade pool for the VPC pair")
	errMasqStateMalformed = errors.New("malformed masquerading state")

	// the dataplane prints this line for the IPs it released
	masqWeakRefsDropped = regexp.MustCompile(`^<\d+ weak references dropped>$`)
)

// the ports of one public IP the masquerade NAT allocates from, 1024 to 65535
const masqPortsPerIP = 65535 - 1024 + 1

// masqPoolState is the TCP masquerade pool of one VPC pair, read from `show masquerading state`. An IP with all its
// ports allocated counts as masqPortsPerIP held ports and no range.
type masqPoolState struct {
	HeldPorts int
	// Ranges is the number of ports and port ranges listed
	Ranges int
	Full   bool
}

// ReleasedTo tells whether the pool holds level ports or fewer.
func (s masqPoolState) ReleasedTo(level int) bool {
	return !s.Full && s.HeldPorts <= level
}

// where parseMasqueradeState is in a TCP pool of the VPC pair
type masqSection int

const (
	masqOutside masqSection = iota
	masqInPool
	masqInFreeIPs
	masqInAllocatedIPs
	masqInIP
	masqInPorts
)

// parseMasqueradeState reads the TCP pools of the VPC pair from the output of the dataplane-cli command `show
// masquerading state`. The ports held by every allocated IP of every pool of the pair are summed. The pool is full when
// none of its IPs is free and every allocated one has all its ports allocated. A line it doesn't know in a pool of the
// pair, or a pool without its list of allocated IPs, is an error, so that a format change never reads as 0 held ports.
func parseMasqueradeState(out string, srcVNI, dstVNI uint32) (masqPoolState, error) {
	header := fmt.Sprintf("TCP | source VPC: VNI(%d), dest VPC: VNI(%d), ", srcVNI, dstVNI)

	state := masqPoolState{}
	pools, freeIPs, allocatedIPs, fullIPs := 0, 0, 0, 0
	section := masqOutside
	sawAllocated := false
	ports := ""

	// closes the section being read when a new one starts
	closeSection := func(lineNo int) error {
		switch section {
		case masqInPorts:
			items := 0
			for item := range strings.SplitSeq(ports, ",") {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				items++
				first, last, isRange := strings.Cut(item, "-")
				lo, err := strconv.ParseUint(first, 10, 16)
				if err != nil {
					return fmt.Errorf("%w: line %d: port %q: %w", errMasqStateMalformed, lineNo, item, err)
				}
				hi := lo
				if isRange {
					if hi, err = strconv.ParseUint(last, 10, 16); err != nil {
						return fmt.Errorf("%w: line %d: port range %q: %w", errMasqStateMalformed, lineNo, item, err)
					}
				}
				if hi < lo {
					return fmt.Errorf("%w: line %d: port range %q is reversed", errMasqStateMalformed, lineNo, item)
				}
				state.HeldPorts += int(hi-lo) + 1
				state.Ranges++
			}
			if items == 0 {
				return fmt.Errorf("%w: line %d: empty port list", errMasqStateMalformed, lineNo)
			}
			ports = ""
		case masqInIP:
			return fmt.Errorf("%w: line %d: allocated IP without its ports", errMasqStateMalformed, lineNo)
		case masqOutside, masqInPool, masqInFreeIPs, masqInAllocatedIPs:
		}

		return nil
	}

	lines := strings.Split(out, "\n")
	for idx, raw := range append(lines, "") {
		lineNo := idx + 1
		line := strings.TrimSpace(raw)
		indent := len(raw) - len(strings.TrimLeft(raw, " "))

		if line == "" || indent <= 2 {
			if section != masqOutside {
				if err := closeSection(lineNo); err != nil {
					return masqPoolState{}, err
				}
				if !sawAllocated {
					return masqPoolState{}, fmt.Errorf("%w: line %d: pool without allocated IPs", errMasqStateMalformed, lineNo)
				}
			}
			section = masqOutside
			if indent == 2 && strings.HasPrefix(line, header) {
				pools++
				section = masqInPool
				sawAllocated = false
			}

			continue
		}
		if section == masqOutside {
			continue
		}

		addr, endsWithColon := strings.CutSuffix(line, ":")
		_, addrErr := netip.ParseAddr(addr)
		isAddrLine := endsWithColon && addrErr == nil

		switch {
		case section == masqInPorts && line == "[all ports allocated]" && ports == "":
			fullIPs++
			state.HeldPorts += masqPortsPerIP
			section = masqInAllocatedIPs
		case (section == masqInAllocatedIPs || section == masqInPorts) && masqWeakRefsDropped.MatchString(line):
			if err := closeSection(lineNo); err != nil {
				return masqPoolState{}, err
			}
			section = masqInAllocatedIPs
		case section == masqInPorts && !endsWithColon:
			ports += line
		case strings.HasPrefix(line, "idle timeout: "), strings.HasPrefix(line, "region [") && strings.HasSuffix(line, "]:"):
			if err := closeSection(lineNo); err != nil {
				return masqPoolState{}, err
			}
			section = masqInPool
		case line == "IP ranges in pool:":
			section = masqInFreeIPs
		case line == "allocated IPs:":
			sawAllocated = true
			section = masqInAllocatedIPs
		case section == masqInFreeIPs && strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			freeIPs++
		case section == masqInFreeIPs && line == "(empty)":
		case (section == masqInAllocatedIPs || section == masqInPorts) && isAddrLine:
			if err := closeSection(lineNo); err != nil {
				return masqPoolState{}, err
			}
			allocatedIPs++
			section = masqInIP
		case section == masqInAllocatedIPs && line == "(empty)":
		case section == masqInIP && line == "allocated ports:":
			section = masqInPorts
		default:
			return masqPoolState{}, fmt.Errorf("%w: line %d: unexpected %q", errMasqStateMalformed, lineNo, line)
		}
	}
	if pools == 0 {
		return masqPoolState{}, fmt.Errorf("%w: source VNI %d, destination VNI %d", errMasqPoolNotFound, srcVNI, dstVNI)
	}

	state.Full = freeIPs == 0 && allocatedIPs > 0 && fullIPs == allocatedIPs

	return state, nil
}
