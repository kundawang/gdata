package gdata

import (
	"encoding/base64"
	"errors"
	"syscall/js"

	_ "embed"
)

// Since we can't store objects inside the localStorage,
// we store a metadata string per every object.
// This metadata is a single string consisting of all
// "filenames" (related property keys).

//go:embed "gdata.js"
var jsCode string

type dataManager struct {
	appName string
}

func newDataManager(config Config) (dataManagerImpl, slotStore, error) {
	const evalArgument = `
        let ___result = true;
        try {
            const ___key = "storage__test__";
            window.localStorage.setItem(___key, null);
            window.localStorage.removeItem(___key);
        } catch (e) {
            ___result = false;
        }
        ___result`
	hasLocalStorage := js.Global().Get("window").Call("eval", evalArgument).Bool()
	if !hasLocalStorage {
		return nil, nil, errors.New("localStorage is not available")
	}
	m := &dataManager{
		appName: config.AppName,
	}
	lib := js.Global().Get("window").Call("eval", jsCode)
	js.Global().Set("___gdata", lib)
	return m, &jsSlotStore{appName: config.AppName}, nil
}

func (m *dataManager) getLib() js.Value {
	return js.Global().Get("___gdata")
}

func (m *dataManager) ObjectPropPath(objectKey, propKey string) string {
	return m.getLib().Call("objectPropPath", m.appName, objectKey, propKey).String()
}

func (m *dataManager) ListObjectProps(objectKey string) ([]string, error) {
	v := m.getLib().Call("listObjectProps", m.appName, objectKey)
	if v.IsNull() {
		return nil, nil
	}
	result := make([]string, v.Length())
	for i := 0; i < v.Length(); i++ {
		result[i] = v.Index(i).String()
	}
	return result, nil
}

func (m *dataManager) SaveObjectProp(objectKey, propKey string, data []byte) error {
	m.getLib().Call("saveObjectProp", m.appName, objectKey, propKey, string(data))
	return nil
}

func (m *dataManager) LoadObjectProp(objectKey, propKey string) ([]byte, error) {
	result := m.getLib().Call("loadObjectProp", m.appName, objectKey, propKey)
	if result.IsNull() {
		return nil, nil
	}
	return []byte(result.String()), nil
}

func (m *dataManager) ReadObjectProp(objectKey, propKey string, buf []byte) (int, error) {
	result := m.getLib().Call("loadObjectProp", m.appName, objectKey, propKey)
	if result.IsNull() {
		return 0, nil
	}
	// TODO: can we use something like js.CopyBytesToGo() to avoid
	// the extra data copying here? String() would allocated a full-size []byte.
	asString := result.String()
	n := copy(buf, asString)
	return n, nil
}

func (m *dataManager) ObjectPropExists(objectKey, propKey string) bool {
	return m.getLib().Call("objectPropExists", m.appName, objectKey, propKey).Bool()
}

func (m *dataManager) ObjectExists(objectKey string) bool {
	return m.getLib().Call("objectExists", m.appName, objectKey).Bool()
}

func (m *dataManager) DeleteObjectProp(objectKey, propKey string) error {
	m.getLib().Call("deleteObjectProp", m.appName, objectKey, propKey)
	return nil
}

func (m *dataManager) DeleteObject(objectKey string) error {
	m.getLib().Call("deleteObject", m.appName, objectKey)
	return nil
}

// jsSlotStore adapts the localStorage-backed primitives exposed by gdata.js
// to the slotStore interface.
//
// localStorage only stores UTF-16 strings, so binary slot frames are
// transported as standard base64. A single setItem is atomic from the
// page's point of view, which gives the browser backend the same
// all-or-nothing commit semantics as rename() on the filesystem backend.
//
// JavaScript is single-threaded and a single page owns one Manager, so
// SlotLock is a no-op; in-process concurrency is serialized by the engine.
type jsSlotStore struct {
	appName string
}

func (s *jsSlotStore) lib() js.Value {
	return js.Global().Get("___gdata")
}

func (s *jsSlotStore) SlotWrite(relPath string, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	result := s.lib().Call("slotWrite", s.appName, relPath, encoded)
	if !result.Get("ok").Bool() {
		if result.Get("noSpace").Bool() {
			return errLocalStorageQuota
		}
		return errors.New("localStorage write failed: " + result.Get("message").String())
	}
	return nil
}

func (s *jsSlotStore) SlotRead(relPath string) ([]byte, error) {
	result := s.lib().Call("slotRead", s.appName, relPath)
	if !result.Get("ok").Bool() {
		return nil, errors.New("localStorage read failed")
	}
	if result.Get("missing").Bool() {
		return nil, nil
	}
	encoded := result.Get("value").String()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrCorrupted
	}
	return decoded, nil
}

func (s *jsSlotStore) SlotRemove(relPath string) error {
	s.lib().Call("slotRemove", s.appName, relPath)
	return nil
}

func (s *jsSlotStore) SlotRemoveAll(relDir string) error {
	s.lib().Call("slotRemoveAll", s.appName, relDir)
	return nil
}

func (s *jsSlotStore) SlotList(relDir string) ([]string, error) {
	result := s.lib().Call("slotList", s.appName, relDir)
	namesVal := result.Get("names")
	names := make([]string, namesVal.Length())
	for i := 0; i < namesVal.Length(); i++ {
		names[i] = namesVal.Index(i).String()
	}
	return names, nil
}

func (s *jsSlotStore) SlotLock(relPath string) (func(), error) {
	return func() {}, nil
}
