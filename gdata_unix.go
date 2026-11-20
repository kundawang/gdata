//go:build (darwin || linux) && !android

package gdata

import (
	"os"
	"path/filepath"
)

func newDataManager(config Config) (dataManagerImpl, slotStore, error) {
	var dataPath string
	if config.Root != "" {
		dataPath = config.Root
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}
		dataPath = filepath.Join(home, ".local", "share", config.AppName)
		if err := mkdirAll(dataPath); err != nil {
			return nil, nil, err
		}
	}
	m := newFilesystemDataManager(dataPath)
	return m, m, nil
}
