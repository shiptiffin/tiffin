//go:build unix

package cli

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// rootOnly checks that path is root's alone: owned by root, not a
// symlink, and not writable by its group or anyone else.
func rootOnly(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !ok:
		return errors.New("cannot tell who owns " + path)
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink", path)
	case st.Uid != rootUID:
		return fmt.Errorf("%s is not owned by root", path)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s is writable by others than root", path)
	}
	return nil
}
