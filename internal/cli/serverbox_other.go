//go:build !unix

package cli

import "errors"

func rootOnly(string) error { return errors.New("unsupported") }
