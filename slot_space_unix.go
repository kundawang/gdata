//go:build darwin || linux

package gdata

import (
	"errors"
	"os"
	"syscall"
)

// syncSlotDir flushes directory metadata so a rename survives a power loss.
func syncSlotDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	_ = f.Sync()
}

func init() {
	// ENOSPC covers local filesystems; EDQUOT additionally covers
	// quota-based and networked storage setups.
	noSpaceHooks = append(noSpaceHooks, func(err error) bool {
		return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
	})
}
