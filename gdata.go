package gdata

import (
	"errors"
	"fmt"
)

// Manager implements the main gamedata operations.
// You can create a new manager by using Open function.
type Manager struct {
	impl  dataManagerImpl
	slots *slotEngine
}

// dataManagerImpl is an interface every platform-specific storage provider implements.
type dataManagerImpl interface {
	ObjectPropPath(objectKey, propKey string) string

	ListObjectProps(objectKey string) ([]string, error)

	SaveObjectProp(objectKey, propKey string, data []byte) error

	LoadObjectProp(objectKey, propKey string) ([]byte, error)
	ReadObjectProp(objectKey, propKey string, buf []byte) (int, error)

	ObjectPropExists(objectKey, propKey string) bool
	ObjectExists(objectKey string) bool

	DeleteObjectProp(objectKey, propKey string) error
	DeleteObject(objectKey string) error
}

// Config affects the created gamedata manager behavior.
type Config struct {
	// AppName is used as a part of the key used to store the game data.
	//
	// The exact effect depends on the platform, but generally it doesn't have
	// to reflect the application name perfectly.
	//
	// You need to use the same AppName to make sure that the game can
	// then load the previously saved data.
	// If you want to separate the data, use suffixes: "app" and "app2" data
	// will be stored completely independently.
	//
	// An empty app name is not allowed.
	AppName string

	// Root is an optional storage path override.
	//
	// By default, an platform-specific path will be inferred,
	// but some use cases like Steam Cloud may require you
	// to specify a path known ahead of time.
	// This option can be used to store the data in
	// a folder of your choice instead of some ~/.local/share/$AppName.
	// This folder needs to exist!
	//
	// It's ignored on environments like JS/wasm.
	Root string

	// Slots enables the crash-safe save slots and sets how many complete
	// saves per object property are retained.
	//
	// A zero value (the default) keeps the legacy single-file behavior
	// unchanged and disables the slot-aware API (it returns ErrSlotsDisabled).
	// A positive value between 1 and MaxSlots keeps that many recent
	// complete saves. Every save is written into a new validated slot and
	// atomically promoted, so an interrupted write can never corrupt the
	// previously committed save.
	//
	// The behavior is identical on desktop, Android and browser targets;
	// only the underlying storage primitives differ.
	Slots int
}

// Open attempts to create a gamedata manager.
//
// There are various cases when it can fail and you need to be
// ready to handle that situation and run the game without the save states.
// For instance, on wasm platforms it's using a localStorage which can be disabled.
// In this case, a non-nil error will be returned and the game should continue
// without any attempts to load or save data.
//
// One gamedata manager per game is enough.
// You need to pass it explicitely as a part of your game's context.
func Open(config Config) (*Manager, error) {
	if config.AppName == "" {
		return nil, errors.New("config.AppName can't be empty")
	}
	if config.Slots < 0 {
		return nil, errors.New("config.Slots can't be negative")
	}
	if config.Slots > MaxSlots {
		return nil, fmt.Errorf("config.Slots can't exceed %d", MaxSlots)
	}
	m, store, err := newDataManager(config)
	if err != nil {
		return nil, err
	}
	manager := &Manager{impl: m}
	if config.Slots > 0 {
		manager.slots = &slotEngine{
			store:     store,
			slotCount: config.Slots,
		}
	}
	return manager, nil
}

// ListObjectProps returns all object property keys.
//
// If there is no such object, a nil slice is returned.
//
// The returned error is usually a file operation error.
func (m *Manager) ListObjectProps(objectKey string) ([]string, error) {
	return m.impl.ListObjectProps(objectKey)
}

// SaveItem writes to the object's property with the associated key.
//
// Using an empty propKey is allowed.

// Saving to an existing key overwrites it.
// If object did not exist, it will be created automatically.
//
// The returned error is usually a file write error.
func (m *Manager) SaveObjectProp(objectKey, propKey string, data []byte) error {
	return m.impl.SaveObjectProp(objectKey, fixPropKey(propKey), data)
}

// LoadObjectProp reads from the object's property using the provided key.
//
// Using an empty propKey is allowed.
//
// Loading a non-existing propKey is not an error, a nil slice will be returned.
// The same rule applies to a non-existing object.
//
// If you want to know whether some object or a property exists,
// use ObjectExists or ObjectPropExists method.
//
// The returned error is usually a file read error.
func (m *Manager) LoadObjectProp(objectKey, propKey string) ([]byte, error) {
	return m.impl.LoadObjectProp(objectKey, fixPropKey(propKey))
}

// ReadObjectProp is like LoadObjectProp, but reads only up
// to the specified size (in bytes). This size is specified by the buf's len.
//
// This is usually useful on FS-like environments where save files
// can be large, but the header reading operation is needed to perform
// an efficient list-like option.
//
// It can also be useful if you want to reuse the destination buffer for
// reading smaller data into a single reusable buffer.
//
// The size is usually a fixed-size header block size.
// If you have a dynamic header size, there should be a way for
// you to know the size before calling this function (e.g. put it in the propKey).
//
// If data is smaller than size, the returned byte slice will have a length
// equal to the actual data size read.
func (m *Manager) ReadObjectProp(objectKey, propKey string, buf []byte) (int, error) {
	return m.impl.ReadObjectProp(objectKey, fixPropKey(propKey), buf)
}

// ObjectExists reports whether the object was saved before.
func (m *Manager) ObjectExists(objectKey string) bool {
	return m.impl.ObjectExists(objectKey)
}

// ObjectPropExists reports whether an object has the specified property.
//
// Using an empty propKey is allowed.
//
// If object itself doesn't exists, the function will report false.
func (m *Manager) ObjectPropExists(objectKey, propKey string) bool {
	return m.impl.ObjectPropExists(objectKey, fixPropKey(propKey))
}

// DeleteObject removes the object along with all of its properties (if any).
//
// Be careful with this function: it removes the data permanently.
// There is no way to undo it.
//
// Trying to delete a non-existing object is not an error.
//
// The returned error is usually a file operation error.
func (m *Manager) DeleteObject(objectKey string) error {
	if m.slots != nil {
		// Keep the slot area consistent with the legacy object folder.
		// A cleanup failure is reported but does not prevent the legacy
		// removal below; orphaned slots are harmless and can be pruned later.
		if err := m.slots.DeleteObject(objectKey); err != nil && !errors.Is(err, ErrInvalidKey) {
			return err
		}
	}
	// Since RemoveAll returns a nil error for a non-existing
	// path, we can avoid doing an explicit fileExists call.
	return m.impl.DeleteObject(objectKey)
}

// DeleteObjectProp removes the object's property data.
//
// Using an empty propKey is allowed.
//
// Be careful with this function: it removes the data permanently.
// There is no way to undo it.
//
// Trying to delete a non-existing propKey is not an error.
// If object doesn't exist, it's not an error either.
//
// Deleting the last object's property does not delete
// the object itself. Use DeleteObject if you want to
// delete the object completely.
//
// The returned error is usually a file operation error.
func (m *Manager) DeleteObjectProp(objectKey, propKey string) error {
	propKey = fixPropKey(propKey)
	if m.slots != nil {
		if err := m.slots.DeleteScope(objectKey, propKey); err != nil && !errors.Is(err, ErrInvalidKey) {
			return err
		}
	}
	return m.impl.DeleteObjectProp(objectKey, propKey)
}

// ObjectPropPath returns a unique object property path.
//
// Using an empty propKey is allowed.
//
// On platforms with filesystem storage, it's an absolute file path.
// On other platforms it's just some unique resource identifier.
//
// You can't treat it as a filesystem path unless you know what you're doing.
// It's safe to use it for debugging and for things like map keys.
//
// Note that ObjectPropPath returns a potential data path, it doesn't care if
// the item actually exists or not.
// Use ObjectPropExists() method first if you need to know whether this
// objectKey is used.
func (m *Manager) ObjectPropPath(objectKey, propKey string) string {
	return m.impl.ObjectPropPath(objectKey, fixPropKey(propKey))
}

// SaveSlot writes data into a new safe-save slot and atomically promotes
// it to the most recent complete save. The version string is stored as
// slot metadata alongside the data and is returned by ListSlots and
// LoadLatestSlot; pass "" when no version metadata is needed.
//
// The previous complete saves remain untouched until the new manifest is
// committed, so a power loss or a full disk during the write leaves the
// last good save readable. At most Config.Slots recent saves are kept;
// older ones are pruned after a successful commit.
//
// Possible errors: ErrSlotsDisabled (slots not configured), ErrInvalidKey
// (slot-name traversal attempt), ErrNoSpace, ErrBusy (another save is in
// progress) or the underlying storage error.
func (m *Manager) SaveSlot(objectKey, propKey, version string, data []byte) error {
	if m.slots == nil {
		return ErrSlotsDisabled
	}
	return m.slots.Save(objectKey, fixPropKey(propKey), version, data)
}

// LoadLatestSlot reads and validates the most recent complete save.
//
// If the newest slot is corrupted, older slots are tried automatically
// until a valid one is found. ErrNoSlots is returned when no complete
// save exists or none of them passes validation.
func (m *Manager) LoadLatestSlot(objectKey, propKey string) ([]byte, SlotInfo, error) {
	if m.slots == nil {
		return nil, SlotInfo{}, ErrSlotsDisabled
	}
	return m.slots.LoadLatest(objectKey, fixPropKey(propKey))
}

// LoadSlotVersion loads a specific historical save identified by its
// sequence number (as reported by ListSlots). This is the explicit
// version-selection / rollback entry point.
func (m *Manager) LoadSlotVersion(objectKey, propKey string, seq uint64) ([]byte, SlotInfo, error) {
	if m.slots == nil {
		return nil, SlotInfo{}, ErrSlotsDisabled
	}
	return m.slots.LoadVersion(objectKey, fixPropKey(propKey), seq)
}

// ListSlots returns metadata of all complete slots, newest first.
// Use it together with LoadSlotVersion to let the player pick an older
// save to roll back to.
func (m *Manager) ListSlots(objectKey, propKey string) ([]SlotInfo, error) {
	if m.slots == nil {
		return nil, ErrSlotsDisabled
	}
	return m.slots.List(objectKey, fixPropKey(propKey))
}

func fixPropKey(propKey string) string {
	if propKey == "" {
		return "_objdat"
	}
	return propKey
}
