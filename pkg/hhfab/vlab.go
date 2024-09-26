package hhfab

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

const (
	VLABPCIBridgePrefix  = "pcibr"
	VLABNICsPerPCIBridge = 32
	VLABPCIBridges       = 2
	VLABMaxNICs          = VLABNICsPerPCIBridge * VLABPCIBridges
	VLABBaseSSHPort      = 22000
	VLABBaseDirectPort   = 22100
	VLABTapPrefix        = "hhtap"
	VLABBridge           = "hhbr"
	VLABUUIDPrefix       = "77924ab4-a93b-41d4-928e-"
	VLABUUIDTmpl         = VLABUUIDPrefix + "%012d"
)

type VLAB struct {
	VMs          []VM
	Taps         int
	Passthroughs []string
}

type VM struct {
	ID         int
	Name       string
	Type       VMType
	Restricted bool
	NICs       []string
	Size       VMSize
}

func (c *Config) VLABFromConfig(cfg *VLABConfig) (*VLAB, error) {
	orderedVMNames := slices.Collect(maps.Keys(cfg.VMs))
	slices.SortFunc(orderedVMNames, func(a, b string) int {
		vma, vmb := cfg.VMs[a], cfg.VMs[b]

		if vma.Type != vmb.Type {
			return cmp.Compare(vma.Type, vmb.Type)
		}

		return cmp.Compare(a, b)
	})

	vmIDs := map[string]int{}
	for idx, name := range orderedVMNames {
		vmIDs[name] = idx
	}

	vms := []VM{}
	passthroughs := []string{}
	tapID := 0
	controlID := 0
	for _, name := range orderedVMNames {
		vm := cfg.VMs[name]
		vmID := vmIDs[name]
		maxNICID := uint8(0)
		nics := map[uint8]string{}

		for nicName, nicConfig := range vm.NICs {
			nicID, err := getNICID(nicName)
			if err != nil {
				return nil, fmt.Errorf("getting NIC ID for %q: %w", nicName, err)
			}
			maxNICID = max(maxNICID, nicID)

			nics[nicID] = nicConfig
		}

		if maxNICID >= VLABMaxNICs {
			return nil, fmt.Errorf("too many NICs for VM %q: %d", name, len(nics)) //nolint:goerr113
		}

		paddedNICs := make([]string, int(maxNICID)+1)
		for idx := uint8(0); idx <= maxNICID; idx++ {
			if nic, ok := nics[idx]; ok {
				paddedNICs[idx] = nic
			} else {
				paddedNICs[idx] = NICTypeNoop
			}
		}

		for nicID, nicCfgRaw := range paddedNICs {
			if nicID >= VLABMaxNICs {
				return nil, fmt.Errorf("too many NICs for VM %q: %d", name, len(nics)) //nolint:goerr113
			}

			mac := fmt.Sprintf(VLABMACTmpl, vmID, nicID)

			nicCfgParts := strings.SplitN(nicCfgRaw, NICTypeSep, 2)
			nicType := nicCfgParts[0]
			nicCfg := ""
			if len(nicCfgParts) > 1 {
				nicCfg = nicCfgParts[1]
			}

			netdev := ""
			device := ""
			if nicType == NICTypeNoop || nicType == NICTypeDirect {
				port := getDirectNICPort(vmID, nicID)
				netdev = fmt.Sprintf("socket,udp=127.0.0.1:%d", port)

				if nicCfg != "" {
					parts := strings.SplitN(nicCfg, "/", 2)
					if len(parts) != 2 {
						return nil, fmt.Errorf("invalid NIC config %q for VM %q", nicCfg, name) //nolint:goerr113
					}

					otherVM, otherNIC := parts[0], parts[1]
					otherVMID, ok := vmIDs[otherVM]
					if !ok {
						return nil, fmt.Errorf("unknown VM %q in NIC config %q for VM %q", otherVM, nicCfg, name) //nolint:goerr113
					}

					otherNICID, err := getNICID(otherNIC)
					if err != nil {
						return nil, fmt.Errorf("getting NIC ID for %q: %w", nicCfg, err)
					}

					otherPort := getDirectNICPort(otherVMID, int(otherNICID))
					netdev += fmt.Sprintf(",localaddr=127.0.0.1:%d", otherPort)
				}
			} else if nicType == NICTypeUsernet {
				if vm.Type == VMTypeSwitch {
					return nil, fmt.Errorf("usernet NICs are not supported for switch VM %q", name) //nolint:goerr113
				}

				sshPort := VLABBaseSSHPort + vmID
				// TODO make subnet configurable
				netdev = fmt.Sprintf("user,hostname=%s,hostfwd=tcp:0.0.0.0:%d-:22,net=172.31.%d.0/24,dhcpstart=172.31.%d.10", name, sshPort, vmID, vmID)
				if vm.Type == VMTypeControl && controlID == 0 {
					// TODO use consts and enable for other control VMs
					netdev += ",hostfwd=tcp:0.0.0.0:6443-:6443,hostfwd=tcp:0.0.0.0:31000-:31000"
				}
			} else if nicType == NICTypeManagement {
				if nicCfg != "" {
					mac = nicCfg
				}
				netdev = fmt.Sprintf("tap,ifname=%s%d,script=no,downscript=no", VLABTapPrefix, tapID)
				tapID++
			} else if nicType == NICTypePassthrough {
				if nicCfg == "" {
					return nil, fmt.Errorf("missing NIC config for passthrough NIC %d of VM %q", nicID, name) //nolint:goerr113
				}

				passthroughs = append(passthroughs, nicCfg)
				device = fmt.Sprintf("vfio-pci,host=%s", nicCfg)
			} else {
				return nil, fmt.Errorf("unknown NIC type %q for VM %q", nicType, name) //nolint:goerr113
			}

			if netdev != "" {
				netdev += fmt.Sprintf(",id=eth%02d", nicID)
			}

			if device == "" {
				device = fmt.Sprintf("e1000,netdev=eth%02d,mac=%s", nicID, mac)
			}
			device += fmt.Sprintf(",bus=%s%d,addr=0x%x", VLABPCIBridgePrefix, nicID/VLABNICsPerPCIBridge, nicID%VLABNICsPerPCIBridge)

			nic := ""
			if netdev != "" {
				nic += "-netdev " + netdev + " "
			}
			nic += "-device " + device

			paddedNICs[nicID] = nic
		}

		if vm.Type == VMTypeControl {
			controlID++
		}

		size := cfg.Sizes.Server
		if vm.Type == VMTypeSwitch {
			size = cfg.Sizes.Switch
		} else if vm.Type == VMTypeControl {
			size = cfg.Sizes.Control
		}

		vms = append(vms, VM{
			ID:         vmID,
			Name:       name,
			Type:       vm.Type,
			Restricted: vm.Restricted,
			NICs:       paddedNICs,
			Size:       size,
		})
	}

	return &VLAB{
		VMs:          vms,
		Taps:         tapID,
		Passthroughs: passthroughs,
	}, nil
}

const (
	srvPrefix = "enp2s"
	swMPrefix = "M1"
	swEPrefix = "E1/"
)

func getNICID(nic string) (uint8, error) {
	if nic == swMPrefix {
		return 0, nil
	}

	raw := ""
	if strings.HasPrefix(nic, srvPrefix) {
		raw = nic[len(srvPrefix):]
	} else if strings.HasPrefix(nic, swEPrefix) {
		raw = nic[len(swEPrefix):]
	} else {
		return 0, fmt.Errorf("invalid NIC ID %q", nic) //nolint:goerr113
	}

	v, err := strconv.ParseUint(raw, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("parsing NIC ID %q: %w", nic, err)
	}

	return uint8(v), nil
}

func getDirectNICPort(vmID int, nicID int) int {
	return VLABBaseDirectPort + 100*vmID + nicID
}
