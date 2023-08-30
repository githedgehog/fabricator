package vlab

import (
	"testing"
)

func Test_portIdForName(t *testing.T) {
	tests := []struct {
		port   string
		result int
		error  bool
	}{
		{
			port:   "Management0",
			result: 0,
		},
		{
			port:  "Management1",
			error: true,
		},
		{
			port:  "ManagementX",
			error: true,
		},
		{
			port:  "Management",
			error: true,
		},
		{
			port:   "Ethernet0",
			result: 1,
		},
		{
			port:   "Ethernet1",
			result: 2,
		},
		{
			port:  "EthernetX",
			error: true,
		},
		{
			port:  "Ethernet",
			error: true,
		},
		{
			port:   "nic0/port0",
			result: 0,
		},
		{
			port:   "nic0/port1",
			result: 1,
		},
		{
			port:  "nic0/portX",
			error: true,
		},
		{
			port:  "nic0/port",
			error: true,
		},
		{
			port:  "unsupported",
			error: true,
		},
		{
			port:  "",
			error: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.port, func(t *testing.T) {
			result, err := portIdForName(tt.port)
			if tt.error && err == nil {
				t.Errorf("PortIdForName(%s) expected error, got nil", tt.port)
			}
			if !tt.error && err != nil {
				t.Errorf("PortIdForName(%s) expected no error, got %v", tt.port, err)
			}
			if !tt.error && result != tt.result {
				t.Errorf("PortIdForName(%s) expected %d, got %d", tt.port, tt.result, result)
			}
		})
	}
}
