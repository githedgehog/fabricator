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
	"fmt"
	"testing"
)

func Test_VM_UUID(t *testing.T) {
	tests := []struct {
		id     int
		result string
	}{
		{
			id:     0,
			result: "00000000-0000-0000-0000-000000000000",
		},
		{
			id:     1,
			result: "00000000-0000-0000-0000-000000000001",
		},
		{
			id:     42,
			result: "00000000-0000-0000-0000-000000000042",
		},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("id=%d", tt.id), func(t *testing.T) {
			result := (&VM{ID: tt.id}).UUID()
			if result != tt.result {
				t.Errorf("VM(ID=%d).UUID() expected %s, got %s", tt.id, tt.result, result)
			}
		})
	}
}
