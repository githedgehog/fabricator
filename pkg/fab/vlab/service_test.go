// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
			result, err := portIDForName(tt.port)
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
