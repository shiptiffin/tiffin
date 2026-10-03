//go:build !unix

package cli

import "errors"

func diskSpace(string) (uint64, uint64, error) { return 0, 0, errors.New("unsupported") }
