package gdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Safe-slot related errors. They can be matched with errors.Is.
var (
	// ErrSlotsDisabled is returned by the slot-aware API when the manager
	// was opened with Config.Slots set to 0 (the default).
	ErrSlotsDisabled = errors.New("safe save slots are disabled (set Config.Slots to a positive value)")

	// ErrInvalidKey is returned when an objectKey contains path traversal
	// segments, absolute-path markers or other characters that could escape
	// the managed storage area. This is the slot-name traversal guard.
	ErrInvalidKey = errors.New("invalid storage key")

	// ErrNoSlots means that no complete slot exists for the requested
	// object property yet (or all of them are corrupted and unreadable).
	ErrNoSlots = errors.New("no readable save slot")

	// ErrCorrupted means that a slot frame or a slot manifest failed
	// validation. A corrupted slot is skipped automatically; this error
	// is only surfaced when no valid slot can be found.
	ErrCorrupted = errors.New("corrupted save data")

	// ErrNoSpace is returned when the underlying storage reports that
	// there is not enough space left to write a slot or a manifest.
	// Nothing is committed when this error is returned.
	ErrNoSpace = errors.New("not enough storage space")

	// ErrBusy is returned when a concurrent save (typically another
	// process holding the slot lock) is in progress. The caller is
	// expected to retry later; all previously committed slots stay intact.
	ErrBusy = errors.New("save slot is locked by another operation")
)

const (
	// MaxSlots is the upper bound for Config.Slots.
	MaxSlots = 64

	// slotDirName is the per-object directory holding slot data.
	// It is filtered out from ListObjectProps results.
	slotDirName = ".gslots"
	// slotManifestName is the per-property manifest file/value name.
	slotManifestName = "manifest.json"
	// slotLockName is the advisory lock file living inside a slot scope.
	// It is never a slot and must be ignored by every directory scan.
	slotLockName = "lock"

	// staleSlotLockAge bounds how long a lock may be held before it is
	// considered a leftover of a crashed process and reclaimed.
	staleSlotLockAge = 2 * time.Minute
)

// SlotInfo describes one complete, validated save slot.
type SlotInfo struct {
	// Sequence is a monotonically increasing save number.
	// The most recent complete save always has the highest sequence.
	Sequence uint64
	// Version is the user-provided version metadata associated with the save.
	Version string
	// SavedAt is the time when the slot was committed.
	SavedAt time.Time
}

// slotStore is the storage primitive implemented by every platform backend.
// All paths are relative to the manager's storage root and always use
// forward slashes; implementations translate them to their own conventions.
type slotStore interface {
	// SlotWrite atomically replaces the value at relPath with data
	// (write to a temporary location, then commit).
	SlotWrite(relPath string, data []byte) error
	// SlotRead returns the raw value. A missing value yields (nil, nil, nil).
	SlotRead(relPath string) ([]byte, error)
	SlotRemove(relPath string) error
	// SlotRemoveAll removes a directory and everything inside it.
	SlotRemoveAll(relPath string) error
	// SlotList returns base names of all entries inside relDir.
	// A missing directory yields (nil, nil, nil).
	SlotList(relDir string) ([]string, error)
	// SlotLock acquires an exclusive lock for the given scope.
	// The returned function releases it. ErrBusy is returned when the
	// lock is held by somebody else.
	SlotLock(relPath string) (func(), error)
}

// slotEngine contains the platform-neutral safe-slot logic.
type slotEngine struct {
	// mu serializes all in-process slot operations. The store-level lock
	// additionally guards against other processes; this mutex is what makes
	// concurrent SaveSlot calls from goroutines deterministic.
	mu        sync.Mutex
	store     slotStore
	slotCount int
}

// manifestEntry describes one committed slot inside the manifest.
type manifestEntry struct {
	ID      string `json:"id"`
	Seq     uint64 `json:"seq"`
	Version string `json:"version"`
	SavedAt int64  `json:"saved_at"`
}

// slotManifest is the per-property ordered list of complete slots.
// Entries[0], when present, is the most recent complete save.
type slotManifest struct {
	NextSeq uint64          `json:"next_seq"`
	Entries []manifestEntry `json:"entries"`
}

// validateSlotKey is the slot-name traversal guard. Both keys are
// validated against the same rules on every platform.
func validateSlotKey(k string) error {
	if k == "" {
		return fmt.Errorf("%w: empty key", ErrInvalidKey)
	}
	if len(k) > 256 {
		return fmt.Errorf("%w: key is too long", ErrInvalidKey)
	}
	if strings.ContainsAny(k, "\\\x00") {
		return fmt.Errorf("%w: key contains forbidden characters", ErrInvalidKey)
	}
	if strings.HasPrefix(k, "/") {
		return fmt.Errorf("%w: absolute paths are not allowed", ErrInvalidKey)
	}
	for _, segment := range strings.Split(k, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%w: key contains an empty, . or .. path segment", ErrInvalidKey)
		}
	}
	return nil
}

// slotScopeRel returns the storage-relative directory of one property's slots:
// "<objectKey>/.gslots/<propKey>".
func slotScopeRel(objectKey, propKey string) (string, error) {
	if err := validateSlotKey(objectKey); err != nil {
		return "", err
	}
	if err := validateSlotKey(propKey); err != nil {
		return "", err
	}
	return objectKey + "/" + slotDirName + "/" + propKey, nil
}

func slotManifestRel(scope string) string {
	return scope + "/" + slotManifestName
}

// Save writes data into a new slot and atomically promotes it to be the
// most recent complete save.
//
// Recovery order on failure:
//  1. everything before the manifest commit happens on temporary/new slot
//     files, so a crash leaves the previous manifest fully readable;
//  2. a failed manifest commit leaves a new slot on disk as an orphan which
//     is reclaimed by the next save (or by a manifest rebuild);
//  3. only commits that fully succeeded are ever loaded.
func (e *slotEngine) Save(objectKey, propKey, version string, data []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	scope, err := slotScopeRel(objectKey, propKey)
	if err != nil {
		return err
	}

	unlock, err := e.store.SlotLock(scope)
	if err != nil {
		return err
	}
	defer unlock()

	manifest, err := e.loadManifest(scope)
	if err != nil {
		return err
	}

	// Clean up leftovers of interrupted writes before touching anything.
	if err := e.pruneOrphans(scope, manifest); err != nil {
		return err
	}

	seq := manifest.NextSeq
	if seq == 0 {
		seq = 1
	}

	id := e.pickSlotID(seq)
	frame, err := marshalSlotFrame(seq, version, data)
	if err != nil {
		return err
	}

	// Step 1: write the new slot completely. A failure here commits nothing.
	if err := e.store.SlotWrite(scope+"/"+id, frame); err != nil {
		return mapStoreError(err)
	}

	// Step 2: atomically commit the manifest. This single operation is the
	// crash-safe commit point: before it succeeds, readers see the old state.
	newEntry := manifestEntry{
		ID:      id,
		Seq:     seq,
		Version: version,
		SavedAt: time.Now().Unix(),
	}
	updated := slotManifest{
		NextSeq: seq + 1,
		Entries: append([]manifestEntry{newEntry}, manifest.Entries...),
	}

	kept := e.slotCount
	if len(updated.Entries) > kept {
		updated.Entries = updated.Entries[:kept]
	}
	if err := e.writeManifest(scope, &updated); err != nil {
		return mapStoreError(err)
	}

	// Step 3: best-effort removal of slots that fell out of the retention
	// window. Failures here never undo the committed save.
	e.collectGarbage(scope, &updated)

	return nil
}

// LoadLatest reads and validates the most recent complete save.
// Corrupted slots are skipped transparently until a valid one is found;
// ErrNoSlots is returned only when nothing readable remains.
func (e *slotEngine) LoadLatest(objectKey, propKey string) ([]byte, SlotInfo, error) {
	infos, err := e.List(objectKey, propKey)
	if err != nil {
		return nil, SlotInfo{}, err
	}
	for _, info := range infos {
		payload, err := e.readSlot(objectKey, propKey, info)
		if err != nil {
			// A damaged slot must never block an older good one.
			continue
		}
		return payload, info, nil
	}
	return nil, SlotInfo{}, fmt.Errorf("%w: no readable slot for %s/%s", ErrNoSlots, objectKey, propKey)
}

// LoadVersion loads the slot carrying exactly the requested sequence number.
func (e *slotEngine) LoadVersion(objectKey, propKey string, seq uint64) ([]byte, SlotInfo, error) {
	infos, err := e.List(objectKey, propKey)
	if err != nil {
		return nil, SlotInfo{}, err
	}
	for _, info := range infos {
		if info.Sequence != seq {
			continue
		}
		payload, err := e.readSlot(objectKey, propKey, info)
		if err != nil {
			return nil, SlotInfo{}, err
		}
		return payload, info, nil
	}
	return nil, SlotInfo{}, fmt.Errorf("%w: slot sequence %d not found", ErrNoSlots, seq)
}

// List returns metadata of all complete slots, newest first.
func (e *slotEngine) List(objectKey, propKey string) ([]SlotInfo, error) {
	scope, err := slotScopeRel(objectKey, propKey)
	if err != nil {
		return nil, err
	}
	manifest, err := e.loadManifest(scope)
	if err != nil {
		return nil, err
	}
	result := make([]SlotInfo, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		result = append(result, SlotInfo{
			Sequence: entry.Seq,
			Version:  entry.Version,
			SavedAt:  time.Unix(entry.SavedAt, 0),
		})
	}
	return result, nil
}

// DeleteScope removes every slot of one property.
func (e *slotEngine) DeleteScope(objectKey, propKey string) error {
	scope, err := slotScopeRel(objectKey, propKey)
	if err != nil {
		return err
	}
	if err := e.store.SlotRemoveAll(scope); err != nil {
		return mapStoreError(err)
	}
	return nil
}

// DeleteObject removes all slots of every property of one object.
func (e *slotEngine) DeleteObject(objectKey string) error {
	if err := validateSlotKey(objectKey); err != nil {
		return err
	}
	if err := e.store.SlotRemoveAll(objectKey + "/" + slotDirName); err != nil {
		return mapStoreError(err)
	}
	return nil
}

func (e *slotEngine) readSlot(objectKey, propKey string, info SlotInfo) ([]byte, error) {
	scope, err := slotScopeRel(objectKey, propKey)
	if err != nil {
		return nil, err
	}
	// Resolve the slot id through the manifest rather than building a name
	// from caller input.
	manifest, err := e.loadManifest(scope)
	if err != nil {
		return nil, err
	}
	id := ""
	for _, entry := range manifest.Entries {
		if entry.Seq == info.Sequence {
			id = entry.ID
			break
		}
	}
	if id == "" {
		return nil, fmt.Errorf("%w: slot %d disappeared from the manifest", ErrCorrupted, info.Sequence)
	}

	raw, err := e.store.SlotRead(scope + "/" + id)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: slot file %s is missing", ErrCorrupted, id)
	}
	seq, _, payload, err := unmarshalSlotFrame(raw)
	if err != nil {
		return nil, err
	}
	if seq != info.Sequence {
		return nil, fmt.Errorf("%w: slot sequence mismatch (manifest %d, frame %d)", ErrCorrupted, info.Sequence, seq)
	}
	return payload, nil
}

func (e *slotEngine) writeManifest(scope string, manifest *slotManifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "\t")
	if err != nil {
		return err
	}
	return e.store.SlotWrite(slotManifestRel(scope), encoded)
}

// loadManifest reads the manifest. A missing manifest means "no saves yet".
// A corrupted manifest triggers a rebuild from whatever slot files survive,
// which is the self-healing path after a torn manifest write (the manifest
// itself is atomically replaced, so damage here only comes from an
// outside source or an exotic filesystem failure).
func (e *slotEngine) loadManifest(scope string) (*slotManifest, error) {
	raw, err := e.store.SlotRead(slotManifestRel(scope))
	if err != nil {
		return nil, mapStoreError(err)
	}
	if raw == nil {
		return &slotManifest{}, nil
	}

	manifest := &slotManifest{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(manifest); err != nil {
		rebuilt, rebuildErr := e.rebuildManifest(scope)
		if rebuildErr != nil {
			return nil, fmt.Errorf("%w: manifest is unreadable and rebuild failed: %v (decode error: %v)",
				ErrCorrupted, rebuildErr, err)
		}
		return rebuilt, nil
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: manifest contains trailing data", ErrCorrupted)
	}
	if manifest.NextSeq == 0 && len(manifest.Entries) > 0 {
		return nil, fmt.Errorf("%w: manifest has entries but no sequence counter", ErrCorrupted)
	}
	return manifest, nil
}

// rebuildManifest scans the scope directory, validates every slot frame and
// reconstructs the manifest from survivors, newest first.
func (e *slotEngine) rebuildManifest(scope string) (*slotManifest, error) {
	names, err := e.store.SlotList(scope)
	if err != nil {
		return nil, mapStoreError(err)
	}
	entries := make([]manifestEntry, 0, len(names))
	var maxSeq uint64
	for _, name := range names {
		if isInternalScopeName(name) {
			continue
		}
		raw, err := e.store.SlotRead(scope + "/" + name)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if raw == nil {
			continue
		}
		seq, version, _, err := unmarshalSlotFrame(raw)
		if err != nil {
			// Skip damaged frames but keep scanning for good ones.
			continue
		}
		entries = append(entries, manifestEntry{
			ID:      name,
			Seq:     seq,
			Version: version,
			SavedAt: 0,
		})
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Seq > entries[j].Seq
	})
	manifest := &slotManifest{
		NextSeq: maxSeq + 1,
		Entries: entries,
	}
	// Persist the healed manifest so the next load is cheap.
	if err := e.writeManifest(scope, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// isInternalScopeName reports entries that belong to the slot machinery
// and must never be interpreted as slot frames.
func isInternalScopeName(name string) bool {
	return name == slotManifestName || name == slotLockName || strings.HasPrefix(name, "tmp-")
}

// isProtectedScopeName reports entries that cleanup passes must never delete.
// tmp-* leftovers are not protected: they are reclaimed on the next save.
func isProtectedScopeName(name string) bool {
	return name == slotManifestName || name == slotLockName
}

// pruneOrphans removes slot files present on storage but absent from the
// manifest (interrupted-write leftovers) and stale temporary files.
func (e *slotEngine) pruneOrphans(scope string, manifest *slotManifest) error {
	names, err := e.store.SlotList(scope)
	if err != nil {
		return mapStoreError(err)
	}
	known := make(map[string]bool, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		known[entry.ID] = true
	}
	for _, name := range names {
		if isProtectedScopeName(name) || known[name] {
			continue
		}
		if err := e.store.SlotRemove(scope + "/" + name); err != nil {
			return mapStoreError(err)
		}
	}
	return nil
}

// collectGarbage deletes slot files that are no longer referenced after
// retention pruning. Errors are intentionally swallowed: stale extra copies
// do not affect correctness and get cleaned on the next save.
func (e *slotEngine) collectGarbage(scope string, manifest *slotManifest) {
	names, err := e.store.SlotList(scope)
	if err != nil {
		return
	}
	known := make(map[string]bool, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		known[entry.ID] = true
	}
	for _, name := range names {
		if isProtectedScopeName(name) || known[name] {
			continue
		}
		_ = e.store.SlotRemove(scope + "/" + name)
	}
}

// pickSlotID derives the new slot name from the monotonically increasing
// sequence number. Sequence-based names never collide with existing slot
// files, even after a manifest rebuild, and an interrupted write simply
// leaves an orphan file that pruneOrphans knows how to reclaim.
func (e *slotEngine) pickSlotID(seq uint64) string {
	return fmt.Sprintf("slot-%d", seq)
}

// noSpaceHooks is extended by platform-specific files (and by tests) to
// recognize the storage-full errors produced by each backend.
var noSpaceHooks []func(error) bool

func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	for _, hook := range noSpaceHooks {
		if hook(err) {
			return fmt.Errorf("%w: %v", ErrNoSpace, err)
		}
	}
	return err
}
