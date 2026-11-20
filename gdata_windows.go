package gdata

import (
	"errors"
	"os"
	"path/filepath"
)

func newDataManager(config Config) (dataManagerImpl, slotStore, error) {
	var dataPath string
	if config.Root != "" {
		dataPath = config.Root
	} else {
		appData := os.Getenv("AppData")
		if appData == "" {
			return nil, nil, errors.New("AppData env var is undefined")
		}
		dataPath = filepath.Join(appData, config.AppName)
		if err := mkdirAll(dataPath); err != nil {
			return nil, nil, err
		}
	}
	m := newFilesystemDataManager(dataPath)
	return m, m, nil
}
