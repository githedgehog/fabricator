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
