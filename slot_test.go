package gdata

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestSlotFrameRoundTrip(t *testing.T) {
	cases := []struct {
		seq     uint64
		version string
		payload []byte
	}{
		{1, "v1", []byte("hello")},
		{42, "save-2026-11-21", make([]byte, 4096)},
		{7, "", []byte{0, 1, 2, 3, 255, 0}},
		{1<<32 + 3, "версия/日本語", []byte("binary-ish payload")},
	}
	for i, tc := range cases {
		frame, err := marshalSlotFrame(tc.seq, tc.version, tc.payload)
		if err != nil {
			t.Fatalf("case %d: marshal: %v", i, err)
		}
		gotSeq, gotVer, gotPayload, err := unmarshalSlotFrame(frame)
		if err != nil {
			t.Fatalf("case %d: unmarshal: %v", i, err)
		}
		if gotSeq != tc.seq || gotVer != tc.version || !bytes.Equal(gotPayload, tc.payload) {
			t.Fatalf("case %d: frame mismatch", i)
		}
	}
}

func TestSlotFrameCorruptionDetection(t *testing.T) {
	good, err := marshalSlotFrame(3, "v3", []byte("payload-bytes"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("truncated", func(t *testing.T) {
		for _, cut := range []int{0, 4, slotFrameMinSize - 1, len(good) - 1} {
			if _, _, _, err := unmarshalSlotFrame(good[:cut]); !errors.Is(err, ErrCorrupted) {
				t.Fatalf("cut=%d: want ErrCorrupted, got %v", cut, err)
			}
		}
	})

	t.Run("bad_magic", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad[0] = 'X'
		if _, _, _, err := unmarshalSlotFrame(bad); !errors.Is(err, ErrCorrupted) {
			t.Fatalf("want ErrCorrupted, got %v", err)
		}
	})

	t.Run("bad_frame_version", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad[5] = 99
		if _, _, _, err := unmarshalSlotFrame(bad); !errors.Is(err, ErrCorrupted) {
			t.Fatalf("want ErrCorrupted, got %v", err)
		}
	})

	t.Run("checksum_flip", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		// Flip a payload byte; the stored checksum must stop matching.
		bad[len(bad)-1] ^= 0xff
		if _, _, _, err := unmarshalSlotFrame(bad); !errors.Is(err, ErrCorrupted) {
			t.Fatalf("want ErrCorrupted, got %v", err)
		}
	})

	t.Run("length_mismatch", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad = append(bad, 0)
		if _, _, _, err := unmarshalSlotFrame(bad); !errors.Is(err, ErrCorrupted) {
			t.Fatalf("want ErrCorrupted, got %v", err)
		}
	})
}

func TestSlotKeyTraversalGuard(t *testing.T) {
	bad := []string{
		"",
		".",
		"..",
		"../etc/passwd",
		"foo/../bar",
		"foo/..",
		"foo//bar",
		"foo/",
		"/absolute/path",
		"back\\slash",
		"nul\x00byte",
		stringsRepeat("a", 257),
	}
	for _, key := range bad {
		if err := validateSlotKey(key); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("key %q: want ErrInvalidKey, got %v", key, err)
		}
	}

	good := []string{
		"save1",
		"saves/slot-1",
		"a.b/c-d_e",
		"...",       // a single segment of dots is not ".."
		"foo/..bar", // not the ".." segment
		stringsRepeat("a", 256),
	}
	for _, key := range good {
		if err := validateSlotKey(key); err != nil {
			t.Fatalf("key %q: unexpected error %v", key, err)
		}
	}
}

func stringsRepeat(s string, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = s[0]
	}
	return string(out)
}

func TestSlotSaveLoadAndRetention(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 3)

	if _, _, err := engine.LoadLatest("obj", "_objdat"); !errors.Is(err, ErrNoSlots) {
		t.Fatalf("empty store: want ErrNoSlots, got %v", err)
	}

	versions := []string{"v1", "v2", "v3", "v4", "v5"}
	for i, v := range versions {
		payload := []byte("data-" + v)
		if err := engine.Save("obj", "_objdat", v, payload); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	// Only the newest 3 complete saves are retained.
	infos, err := engine.List("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 3 {
		t.Fatalf("want 3 retained slots, got %d (%+v)", len(infos), infos)
	}
	for i, wantSeq := range []uint64{5, 4, 3} {
		if infos[i].Sequence != wantSeq {
			t.Fatalf("slot %d: want seq %d, got %d", i, wantSeq, infos[i].Sequence)
		}
		if infos[i].Version != versions[wantSeq-1] {
			t.Fatalf("slot %d: wrong version %q", i, infos[i].Version)
		}
	}

	// Latest load returns the newest payload.
	payload, info, err := engine.LoadLatest("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if info.Sequence != 5 || string(payload) != "data-v5" {
		t.Fatalf("latest load mismatch: %+v %q", info, payload)
	}

	// Explicit version selection reaches older retained saves.
	for _, seq := range []uint64{3, 4, 5} {
		payload, info, err := engine.LoadVersion("obj", "_objdat", seq)
		if err != nil {
			t.Fatalf("load seq %d: %v", seq, err)
		}
		want := fmt.Sprintf("data-v%d", seq)
		if string(payload) != want || info.Sequence != seq {
			t.Fatalf("seq %d: got %q / %+v", seq, payload, info)
		}
	}
	if _, _, err := engine.LoadVersion("obj", "_objdat", 2); !errors.Is(err, ErrNoSlots) {
		t.Fatalf("pruned seq: want ErrNoSlots, got %v", err)
	}
}

func TestSlotWriteInterruptionLeavesPreviousSave(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 3)

	if err := engine.Save("obj", "_objdat", "v1", []byte("first")); err != nil {
		t.Fatal(err)
	}

	// Fault 1: the new slot frame cannot be written. The manifest must be
	// untouched and the previous save stays readable.
	store.failWritePath = "obj/.gslots/_objdat/slot-"
	store.failErr = errInjected
	if err := engine.Save("obj", "_objdat", "v2", []byte("second")); !errors.Is(err, errInjected) {
		t.Fatalf("want injected error, got %v", err)
	}
	store.failWritePath = ""

	payload, info, err := engine.LoadLatest("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "first" || info.Sequence != 1 {
		t.Fatalf("previous save was lost: %q %+v", payload, info)
	}

	// Fault 2: the slot frame is written but the atomic manifest commit
	// fails. This is the exact "power loss mid-save" shape: an orphan slot
	// exists, the committed save is still the previous one.
	store.failWritePath = "obj/.gslots/_objdat/manifest.json"
	store.failErr = errInjected
	if err := engine.Save("obj", "_objdat", "v2", []byte("second")); !errors.Is(err, errInjected) {
		t.Fatalf("want injected error, got %v", err)
	}
	store.failWritePath = ""

	payload, info, err = engine.LoadLatest("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "first" || info.Sequence != 1 {
		t.Fatalf("manifest failure must not promote a slot: %q %+v", payload, info)
	}

	// The next successful save reclaims the orphan and continues normally.
	// The sequence stays 2: the failed commit never persisted NextSeq, and
	// the orphan slot-2 is pruned before the new slot-2 is written, so the
	// sequence number can never alias a committed slot.
	if err := engine.Save("obj", "_objdat", "v3", []byte("third")); err != nil {
		t.Fatal(err)
	}
	payload, info, err = engine.LoadLatest("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "third" || info.Sequence != 2 {
		t.Fatalf("orphan reclaim failed: %q %+v", payload, info)
	}
	// slot-2 now holds the committed frame; the orphan content was replaced.
	if data, _ := store.SlotRead("obj/.gslots/_objdat/slot-2"); data == nil {
		t.Fatalf("committed slot-2 is missing")
	}
	if _, _, _, err := unmarshalSlotFrame(store.files["obj/.gslots/_objdat/slot-2"]); err != nil {
		t.Fatalf("slot-2 frame invalid: %v", err)
	}
}

func TestSlotNoSpaceMapping(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 2)

	store.failWritePath = "obj/.gslots/_objdat/slot-"
	store.failErr = errInjectedNoSpace
	err := engine.Save("obj", "_objdat", "v1", []byte("x"))
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("want ErrNoSpace, got %v", err)
	}

	// Nothing was committed.
	if infos, _ := engine.List("obj", "_objdat"); len(infos) != 0 {
		t.Fatalf("failed save must not create slots, got %+v", infos)
	}
}

// errInjectedNoSpace is recognized by the test isNoSpaceError hook below.
var errInjectedNoSpace = errors.New("disk full (injected)")

func TestSlotCorruptedLatestRollsBack(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 4)

	for _, v := range []string{"v1", "v2", "v3"} {
		if err := engine.Save("obj", "_objdat", v, []byte("data-"+v)); err != nil {
			t.Fatal(err)
		}
	}

	// Corrupt the newest slot's frame on storage. LoadLatest must skip it
	// and transparently roll back to the previous complete save.
	latestPath := "obj/.gslots/_objdat/slot-3"
	raw, err := store.SlotRead(latestPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xaa
	store.files[latestPath] = raw

	payload, info, err := engine.LoadLatest("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if info.Sequence != 2 || string(payload) != "data-v2" {
		t.Fatalf("rollback failed: %+v %q", info, payload)
	}

	// Explicitly requesting the damaged slot reports the corruption.
	if _, _, err := engine.LoadVersion("obj", "_objdat", 3); !errors.Is(err, ErrCorrupted) {
		t.Fatalf("want ErrCorrupted, got %v", err)
	}

	// A subsequent save cleans up and the slot ring keeps working.
	if err := engine.Save("obj", "_objdat", "v4", []byte("data-v4")); err != nil {
		t.Fatal(err)
	}
	payload, info, err = engine.LoadLatest("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if info.Sequence != 4 || string(payload) != "data-v4" {
		t.Fatalf("recovery save failed: %+v %q", info, payload)
	}
}

func TestSlotCorruptedManifestRebuild(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 3)

	for _, v := range []string{"v1", "v2"} {
		if err := engine.Save("obj", "_objdat", v, []byte("data-"+v)); err != nil {
			t.Fatal(err)
		}
	}

	// Smash the manifest while both slot frames survive (simulates outside
	// damage or a torn manifest write on an exotic backend).
	store.files["obj/.gslots/_objdat/manifest.json"] = []byte("{not-json")

	infos, err := engine.List("obj", "_objdat")
	if err != nil {
		t.Fatalf("manifest rebuild failed: %v", err)
	}
	if len(infos) != 2 || infos[0].Sequence != 2 || infos[1].Sequence != 1 {
		t.Fatalf("rebuilt manifest mismatch: %+v", infos)
	}

	// The rebuilt manifest was persisted and payloads still validate.
	payload, info, err := engine.LoadLatest("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "data-v2" || info.Sequence != 2 {
		t.Fatalf("rebuilt load mismatch: %q %+v", payload, info)
	}

	// Sequence continuity after a rebuild.
	if err := engine.Save("obj", "_objdat", "v3", []byte("data-v3")); err != nil {
		t.Fatal(err)
	}
	payload, info, _ = engine.LoadLatest("obj", "_objdat")
	if info.Sequence != 3 || string(payload) != "data-v3" {
		t.Fatalf("post-rebuild save mismatch: %q %+v", payload, info)
	}
}

func TestSlotAllCorruptedReportsNoSlots(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 2)
	if err := engine.Save("obj", "_objdat", "v1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	raw := store.files["obj/.gslots/_objdat/slot-1"]
	for i := range raw {
		raw[i] = 0
	}
	store.files["obj/.gslots/_objdat/slot-1"] = raw
	if _, _, err := engine.LoadLatest("obj", "_objdat"); !errors.Is(err, ErrNoSlots) {
		t.Fatalf("want ErrNoSlots, got %v", err)
	}
}

func TestSlotBusyLockAndRecovery(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 2)
	scope, err := slotScopeRel("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}

	store.lockBusy[scope] = true
	if err := engine.Save("obj", "_objdat", "v1", []byte("x")); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	store.lockBusy[scope] = false

	// After the lock clears, saves succeed and nothing partial was committed.
	if err := engine.Save("obj", "_objdat", "v1", []byte("x")); err != nil {
		t.Fatalf("retry after busy lock failed: %v", err)
	}
}

func TestSlotConcurrentSaves(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 4)

	const goroutines = 50
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			errs <- engine.Save("obj", "_objdat", fmt.Sprintf("v%d", i), []byte(fmt.Sprintf("payload-%d", i)))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent save error: %v", err)
		}
	}

	infos, err := engine.List("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 4 {
		t.Fatalf("want 4 retained slots, got %d", len(infos))
	}

	// Sequences must be gap-free and strictly ordered.
	wantSeq := uint64(goroutines)
	for i, info := range infos {
		if info.Sequence != wantSeq-uint64(i) {
			t.Fatalf("slot %d: want seq %d, got %d", i, wantSeq-uint64(i), info.Sequence)
		}
		if _, _, err := engine.LoadVersion("obj", "_objdat", info.Sequence); err != nil {
			t.Fatalf("slot %d unreadable: %v", info.Sequence, err)
		}
	}
}

func TestSlotDeleteScopeAndObject(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 3)

	if err := engine.Save("obj", "a", "v1", []byte("a1")); err != nil {
		t.Fatal(err)
	}
	if err := engine.Save("obj", "b", "v1", []byte("b1")); err != nil {
		t.Fatal(err)
	}

	if err := engine.DeleteScope("obj", "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.LoadLatest("obj", "a"); !errors.Is(err, ErrNoSlots) {
		t.Fatalf("deleted scope: want ErrNoSlots, got %v", err)
	}
	if _, _, err := engine.LoadLatest("obj", "b"); err != nil {
		t.Fatalf("sibling scope must survive: %v", err)
	}

	if err := engine.DeleteObject("obj"); err != nil {
		t.Fatal(err)
	}
	if names, _ := store.SlotList("obj/.gslots"); len(names) != 0 {
		t.Fatalf("object slot dir not removed, got %v", names)
	}
}

func TestSlotPruneKeepsInternalFiles(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 2)
	scope, err := slotScopeRel("obj", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	manifest := &slotManifest{
		NextSeq: 2,
		Entries: []manifestEntry{
			{ID: "slot-1", Seq: 1, Version: "v1"},
		},
	}
	// Known slot, an orphan, a tmp leftover and the active lock.
	store.files[scope+"/slot-1"] = []byte("known")
	store.files[scope+"/slot-99"] = []byte("orphan")
	store.files[scope+"/tmp-abcd"] = []byte("partial")
	store.files[scope+"/"+slotLockName] = []byte("held")

	if err := engine.pruneOrphans(scope, manifest); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.files[scope+"/"+slotLockName]; !ok {
		t.Fatal("pruneOrphans removed the active lock file")
	}
	if _, ok := store.files[scope+"/slot-1"]; !ok {
		t.Fatal("pruneOrphans removed a manifest-referenced slot")
	}
	if _, ok := store.files[scope+"/slot-99"]; ok {
		t.Fatal("orphan slot was not pruned")
	}
	if _, ok := store.files[scope+"/tmp-abcd"]; ok {
		t.Fatal("tmp leftover was not pruned")
	}
}

func TestSlotMultipleObjectsAndProps(t *testing.T) {
	store := newMemStore()
	engine := newTestEngine(store, 2)

	if err := engine.Save("obj1", "p", "v1", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := engine.Save("obj2", "p", "v1", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := engine.Save("obj2", "q", "v1", []byte("three")); err != nil {
		t.Fatal(err)
	}

	check := func(obj, prop, want string) {
		t.Helper()
		payload, _, err := engine.LoadLatest(obj, prop)
		if err != nil || string(payload) != want {
			t.Fatalf("%s/%s: got %q, err=%v", obj, prop, payload, err)
		}
	}
	check("obj1", "p", "one")
	check("obj2", "p", "two")
	check("obj2", "q", "three")
}

func TestSlotFixedLegacyFrame(t *testing.T) {
	// A frame produced with the fixed GDS1 layout, hand-built here so the
	// test does not depend on marshalSlotFrame: this guards the on-disk
	// format against accidental changes.
	const (
		seq     = uint64(9)
		version = "fixed-v1"
		payload = "legacy payload"
	)
	frame := mustBuildFixedFrame(t, seq, version, []byte(payload))

	gotSeq, gotVer, gotPayload, err := unmarshalSlotFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if gotSeq != seq || gotVer != version || string(gotPayload) != payload {
		t.Fatalf("fixed frame decode mismatch: %d %q %q", gotSeq, gotVer, gotPayload)
	}

	// And it round-trips through a real engine-backed load.
	store := newMemStore()
	engine := newTestEngine(store, 2)
	scope, err := slotScopeRel("legacy", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	store.files[scope+"/slot-9"] = frame
	store.files[scope+"/manifest.json"] = mustBuildManifest(t, slotManifest{
		NextSeq: 10,
		Entries: []manifestEntry{{ID: "slot-9", Seq: 9, Version: version, SavedAt: 0}},
	})
	data, info, err := engine.LoadLatest("legacy", "_objdat")
	if err != nil {
		t.Fatal(err)
	}
	if info.Sequence != 9 || string(data) != payload {
		t.Fatalf("legacy fixture load mismatch: %+v %q", info, data)
	}
}
