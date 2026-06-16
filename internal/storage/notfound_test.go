package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestIsNotExist(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"fs.ErrNotExist", fs.ErrNotExist, true},
		{"wrapped fs.ErrNotExist", fmt.Errorf("list x: %w", fs.ErrNotExist), true},
		{"other error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsNotExist(tt.err); got != tt.want {
				t.Errorf("IsNotExist(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
