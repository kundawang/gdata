//go:build darwin || linux || windows

package gdata

import (
	"os"
	"path/filepath"
	"time"
)

// SlotWrite implements the crash-safe atomic replacement on top of the
// filesystem: write a temporary file in the same directory, fsync it,
// rename it over the destination and fsync the directory.
//
// Because the rename is atomic, readers either observe the complete old
// value or the complete new value, never a half-written one. A power loss
// before the rename only leaves a tmp-* file behind, which is reclaimed
// on the next save.
func (m *filesystemDataManager) SlotWrite(relPath string, data []byte) error {
	full := filepath.Join(m.dataPath, filepath.FromSlash(relPath))
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	// Flush file contents before the rename so the new directory entry
	// can never point at data that never reached the disk.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}

	if err := os.Rename(tmpPath, full); err != nil {
		cleanup()
		return err
	}
	// Persist the new directory entry itself.
	syncSlotDir(dir)
	return nil
}

func (m *filesystemDataManager) SlotRead(relPath string) ([]byte, error) {
	full := filepath.Join(m.dataPath, filepath.FromSlash(relPath))
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func (m *filesystemDataManager) SlotRemove(relPath string) error {
	full := filepath.Join(m.dataPath, filepath.FromSlash(relPath))
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *filesystemDataManager) SlotRemoveAll(relPath string) error {
	full := filepath.Join(m.dataPath, filepath.FromSlash(relPath))
	// os.RemoveAll returns nil for a non-existing path.
	return os.RemoveAll(full)
}

func (m *filesystemDataManager) SlotList(relDir string) ([]string, error) {
	full := filepath.Join(m.dataPath, filepath.FromSlash(relDir))
	entries, err := os.ReadDir(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

// SlotLock implements an advisory cross-process lock using O_CREATE|O_EXCL.
// In-process concurrency is serialized by slotEngine's own call ordering;
// this lock additionally guards against a second game process saving into
// the same scope. A lock older than staleSlotLockAge is treated as a
// crashed-process leftover and reclaimed.
func (m *filesystemDataManager) SlotLock(relPath string) (func(), error) {
	dir := filepath.Join(m.dataPath, filepath.FromSlash(relPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, slotLockName)

	tryAcquire := func() bool {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return true
		}
		return false
	}

	if tryAcquire() {
		return m.makeSlotUnlock(lockPath), nil
	}

	// The lock exists. Give a live writer a short grace window, then only
	// reclaim it when it is provably stale.
	if info, err := os.Stat(lockPath); err == nil && time.Since(info.ModTime()) > staleSlotLockAge {
		_ = os.Remove(lockPath)
		if tryAcquire() {
			return m.makeSlotUnlock(lockPath), nil
		}
	}

	return nil, ErrBusy
}

func (m *filesystemDataManager) makeSlotUnlock(lockPath string) func() {
	return func() {
		_ = os.Remove(lockPath)
	}
}
