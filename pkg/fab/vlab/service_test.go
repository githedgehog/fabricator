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
			port:  "unsupported",
			error: true,
		},
		{
			port:  "",
			error: true,
		},
		{
			port:  "M0",
			error: true,
		},
		{
			port:   "M1",
			result: 0,
		},
		{
			port:  "M2",
			error: true,
		},
		{
			port:   "E1/1",
			result: 1,
		},
		{
			port:   "E1/2",
			result: 2,
		},
		{
			port:  "E2/1",
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
