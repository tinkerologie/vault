//go:build !linux

package operatorproof

import "errors"

func NewRuntime() (*Verifier, error) {
	return nil, errors.New("native Unraid approval requires Linux")
}
