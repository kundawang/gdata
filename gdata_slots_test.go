package gdata_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quasilyte/gdata/v2"
)

func openSlotManager(t *testing.T, slots int) (*gdata.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := gdata.Open(gdata.Config{
		AppName: "gdata_slots_test",
		Root:    dir,
		Slots:   slots,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m, dir
}

func TestSlotManagerSaveLoad(t *testing.T) {
	m, _ := openSlotManager(t, 3)

	for i := 1; i <= 5; i++ {
		data := []byte("save-number-" + itoa(i))
		if err := m.SaveSlot("obj", "save.dat", "v"+itoa(i), data); err != nil {
			t.Fatalf("SaveSlot %d: %v", i, err)
		}
	}

	infos, err := m.ListSlots("obj", "save.dat")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 3 {
		t.Fatalf("want 3 retained slots, got %d", len(infos))
	}
	if infos[0].Sequence != 5 || infos[0].Version != "v5" {
		t.Fatalf("newest slot mismatch: %+v", infos[0])
	}

	payload, info, err := m.LoadLatestSlot("obj", "save.dat")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "save-number-5" || info.Sequence != 5 {
		t.Fatalf("latest mismatch: %q %+v", payload, info)
	}

	payload, _, err = m.LoadSlotVersion("obj", "save.dat", infos[2].Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "save-number-3" {
		t.Fatalf("version-selected payload mismatch: %q", payload)
	}
}

func TestSlotManagerPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	m, err := gdata.Open(gdata.Config{AppName: "app", Root: dir, Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSlot("obj", "p", "v1", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSlot("obj", "p", "v2", []byte("second")); err != nil {
		t.Fatal(err)
	}

	// Reopen with a brand-new manager, as the game would after a restart.
	m2, err := gdata.Open(gdata.Config{AppName: "app", Root: dir, Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	payload, info, err := m2.LoadLatestSlot("obj", "p")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "second" || info.Version != "v2" || info.Sequence != 2 {
		t.Fatalf("reopened load mismatch: %q %+v", payload, info)
	}
	slots, err := m2.ListSlots("obj", "p")
	if err != nil || len(slots) != 2 {
		t.Fatalf("reopened list mismatch: %v %+v", err, slots)
	}
}

func TestSlotFilesystemLayout(t *testing.T) {
	m, dir := openSlotManager(t, 2)
	if err := m.SaveSlot("saves/run1", "state", "v1", []byte("data")); err != nil {
		t.Fatal(err)
	}

	scope := filepath.Join(dir, "saves", "run1", ".gslots", "state")
	entries, err := os.ReadDir(scope)
	if err != nil {
		t.Fatalf("scope dir missing: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["manifest.json"] || !names["slot-1"] || names["lock"] {
		t.Fatalf("unexpected scope layout: %v", names)
	}

	// No tmp-* leftovers after a clean save.
	var leftovers []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if len(info.Name()) >= 4 && info.Name()[:4] == "tmp-" {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	if len(leftovers) != 0 {
		t.Fatalf("temporary files were not cleaned: %v", leftovers)
	}

	// The internal slot directory is invisible to the legacy listing API.
	props, err := m.ListObjectProps("saves/run1")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range props {
		if p == ".gslots" {
			t.Fatalf(".gslots leaked into ListObjectProps")
		}
	}
}

func TestSlotCorruptionRollsBackOnRealFS(t *testing.T) {
	m, dir := openSlotManager(t, 3)
	if err := m.SaveSlot("obj", "p", "v1", []byte("good-1")); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSlot("obj", "p", "v2", []byte("good-2")); err != nil {
		t.Fatal(err)
	}

	// Corrupt the newest slot directly on disk (simulates torn storage).
	latest := filepath.Join(dir, "obj", ".gslots", "p", "slot-2")
	raw, err := os.ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(latest, raw, 0o666); err != nil {
		t.Fatal(err)
	}

	payload, info, err := m.LoadLatestSlot("obj", "p")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "good-1" || info.Sequence != 1 {
		t.Fatalf("rollback mismatch: %q %+v", payload, info)
	}
}

func TestSlotManifestDamageRebuildOnRealFS(t *testing.T) {
	m, dir := openSlotManager(t, 3)
	if err := m.SaveSlot("obj", "p", "v1", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSlot("obj", "p", "v2", []byte("b")); err != nil {
		t.Fatal(err)
	}

	manifest := filepath.Join(dir, "obj", ".gslots", "p", "manifest.json")
	if err := os.WriteFile(manifest, []byte("{broken"), 0o666); err != nil {
		t.Fatal(err)
	}

	payload, info, err := m.LoadLatestSlot("obj", "p")
	if err != nil {
		t.Fatalf("rebuild should recover readable slots: %v", err)
	}
	if string(payload) != "b" || info.Sequence != 2 {
		t.Fatalf("rebuilt load mismatch: %q %+v", payload, info)
	}
}

func TestSlotInterruptedWriteLeavesTmpFile(t *testing.T) {
	m, dir := openSlotManager(t, 2)
	if err := m.SaveSlot("obj", "p", "v1", []byte("committed")); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash between writing the temp file and the rename:
	// drop an arbitrary tmp-* file into the scope and confirm the next
	// save reclaims it while the committed save stays available.
	scope := filepath.Join(dir, "obj", ".gslots", "p")
	if err := os.WriteFile(filepath.Join(scope, "tmp-crashed"), []byte("partial"), 0o666); err != nil {
		t.Fatal(err)
	}

	payload, _, err := m.LoadLatestSlot("obj", "p")
	if err != nil || string(payload) != "committed" {
		t.Fatalf("committed save must survive tmp leftovers: %q %v", payload, err)
	}

	if err := m.SaveSlot("obj", "p", "v2", []byte("next")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(scope, "tmp-crashed")); !os.IsNotExist(err) {
		t.Fatalf("tmp-crashed should have been pruned, stat err=%v", err)
	}
	payload, info, err := m.LoadLatestSlot("obj", "p")
	if err != nil || string(payload) != "next" || info.Sequence != 2 {
		t.Fatalf("post-crash save mismatch: %q %+v %v", payload, info, err)
	}
}

func TestSlotTraversalRejectedOnRealFS(t *testing.T) {
	m, _ := openSlotManager(t, 2)
	cases := []string{"../escape", "a/../../b", "/abs", "back\\slash"}
	for _, key := range cases {
		if err := m.SaveSlot(key, "p", "v", []byte("x")); !errors.Is(err, gdata.ErrInvalidKey) {
			t.Fatalf("key %q: want ErrInvalidKey, got %v", key, err)
		}
	}
}

func TestSlotDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	m, err := gdata.Open(gdata.Config{AppName: "app", Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSlot("obj", "p", "v", []byte("x")); !errors.Is(err, gdata.ErrSlotsDisabled) {
		t.Fatalf("want ErrSlotsDisabled, got %v", err)
	}
	if _, _, err := m.LoadLatestSlot("obj", "p"); !errors.Is(err, gdata.ErrSlotsDisabled) {
		t.Fatalf("want ErrSlotsDisabled, got %v", err)
	}
	if _, err := m.ListSlots("obj", "p"); !errors.Is(err, gdata.ErrSlotsDisabled) {
		t.Fatalf("want ErrSlotsDisabled, got %v", err)
	}
}

func TestSlotDeleteIntegration(t *testing.T) {
	m, dir := openSlotManager(t, 2)
	if err := m.SaveSlot("obj", "p", "v1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteObjectProp("obj", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "obj", ".gslots", "p")); !os.IsNotExist(err) {
		t.Fatalf("slot scope should be removed with the property")
	}

	if err := m.SaveSlot("obj", "p", "v1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteObject("obj"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "obj", ".gslots")); !os.IsNotExist(err) {
		t.Fatalf("slot dir should be removed with the object")
	}
}

func TestSlotConfigValidation(t *testing.T) {
	if _, err := gdata.Open(gdata.Config{AppName: "app", Slots: -1}); err == nil {
		t.Fatal("negative Slots must be rejected")
	}
	if _, err := gdata.Open(gdata.Config{AppName: "app", Slots: gdata.MaxSlots + 1}); err == nil {
		t.Fatal("over-limit Slots must be rejected")
	}
}

func TestSlotLockHeldByAnotherProcess(t *testing.T) {
	m, dir := openSlotManager(t, 2)

	// Emulate a second process holding the scope lock.
	lockFile := filepath.Join(dir, "obj", ".gslots", "p", "lock")
	if err := os.MkdirAll(filepath.Dir(lockFile), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := m.SaveSlot("obj", "p", "v1", []byte("x")); !errors.Is(err, gdata.ErrBusy) {
		t.Fatalf("want ErrBusy while foreign lock is held, got %v", err)
	}

	// Once the other process releases the lock, saves go through.
	if err := os.Remove(lockFile); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSlot("obj", "p", "v1", []byte("x")); err != nil {
		t.Fatalf("save after lock release: %v", err)
	}

	// And the lock is cleaned up on completion.
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed after save, stat err=%v", err)
	}
}

func TestSlotStaleLockReclaimed(t *testing.T) {
	m, dir := openSlotManager(t, 2)
	lockFile := filepath.Join(dir, "obj", ".gslots", "p", "lock")
	if err := os.MkdirAll(filepath.Dir(lockFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockFile, []byte("crashed"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate the lock well beyond the stale threshold.
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(lockFile, old, old); err != nil {
		t.Fatal(err)
	}

	if err := m.SaveSlot("obj", "p", "v1", []byte("x")); err != nil {
		t.Fatalf("stale lock should be reclaimed, got %v", err)
	}
	payload, _, err := m.LoadLatestSlot("obj", "p")
	if err != nil || string(payload) != "x" {
		t.Fatalf("load after stale-lock reclaim: %q %v", payload, err)
	}
}

func TestSlotLockSurvivesOrphanPruning(t *testing.T) {
	// Regression: the lock file used to coordinate a second process must
	// never be reaped as an orphan slot while a save is in progress.
	m, dir := openSlotManager(t, 2)
	if err := m.SaveSlot("obj", "p", "v1", []byte("first")); err != nil {
		t.Fatal(err)
	}

	// A foreign (fresh, non-stale) lock plus an orphan tmp file.
	scope := filepath.Join(dir, "obj", ".gslots", "p")
	lockFile := filepath.Join(scope, "lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.WriteFile(filepath.Join(scope, "tmp-orphan"), []byte("partial"), 0o666); err != nil {
		t.Fatal(err)
	}

	// The save is rejected because the foreign lock is still held...
	if err := m.SaveSlot("obj", "p", "v2", []byte("x")); !errors.Is(err, gdata.ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	// ...and neither the lock nor the previous save were touched.
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("lock file was removed during a busy save: %v", err)
	}
	payload, _, err := m.LoadLatestSlot("obj", "p")
	if err != nil || string(payload) != "first" {
		t.Fatalf("committed save lost: %q %v", payload, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
