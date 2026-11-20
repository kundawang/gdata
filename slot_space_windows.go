//go:build windows

package gdata

import "syscall"

// syncSlotDir is a no-op on Windows: MoveFileEx (used by os.Rename) already
// provides the durable replacement guarantee and there is no portable
// directory-fsync equivalent.
func syncSlotDir(dir string) {}

const (
	winErrorDiskFull       syscall.Errno = 112 // ERROR_DISK_FULL
	winErrorHandleDiskFull syscall.Errno = 39  // ERROR_HANDLE_DISK_FULL
)

func init() {
	noSpaceHooks = append(noSpaceHooks, func(err error) bool {
		return errorsIsErrno(err, winErrorDiskFull) || errorsIsErrno(err, winErrorHandleDiskFull)
	})
}

// errorsIsErrno mirrors errors.Is for syscall.Errno without depending on
// the Unwrap chain shape used by a particular Go version's os errors.
func errorsIsErrno(err error, target syscall.Errno) bool {
	for err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
