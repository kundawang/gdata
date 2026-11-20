# gdata

![Build Status](https://github.com/quasilyte/gdata/workflows/Go/badge.svg)
[![PkgGoDev](https://pkg.go.dev/badge/mod/github.com/quasilyte/gdata)](https://pkg.go.dev/mod/github.com/quasilyte/gdata)

A gamedata package that provides convenient cross-platform storage for games.

Some examples of such gamedata that you might want to store:

* Game settings
* Save states
* Replays
* Pluging/mods metadata

This package was made with [Ebitengine](https://github.com/hajimehoshi/ebiten/) in mind, but it should be usable with any kind of a game engine for Go.

Platforms supported:

* Windows (file system, AppData)
* Linux (file system, ~/.local/share)
* MacOS (file system, ~/.local/share)
* Android (file system, app data directory)
* Browser/wasm (local storage)

This library tries to use the most conventional app data folder for every platform.

It provides a simple key-value style API. It can be considered to be a platform-agnostic localStorage.

This package was part of my game development framework which I used in all of my Go-powered games. Now I'm feel like it's ready to become a part of the ecosystem.

## Installation

```bash
go get github.com/quasilyte/gdata
```

## Quick Start

```go
package main

import (
	"fmt"

	"github.com/quasilyte/gdata/v2"
)

func main() {
	// m is a data manager; treat it as a connection to a filesystem.
	m, err := gdata.Open(gdata.Config{
		AppName: "mygame",
	})
	if err != nil {
		panic(err)
	}

	if err := m.SaveObjectProp("core", "save.data", []byte("mydata")); err != nil {
		panic(err)
	}
	if err := m.SaveObjectProp("core", "settings.json", []byte("settings")); err != nil {
		panic(err)
	}

	result, err := m.LoadObjectProp("core", "save.data")
	if err != nil {
		panic(err)
	}
	fmt.Println("=>", string(result)) // "mydata"

	fmt.Println(m.ObjectExists("core"))                      // true
	fmt.Println(m.ObjectPropExists("core", "save.data"))     // true
	fmt.Println(m.ObjectPropExists("core", "settings.json")) // true

	{
		propKeys, err := m.ListObjectProps("core")
		if err != nil {
			panic(err)
		}
		for _, p := range propKeys {
			v, err := m.LoadObjectProp("core", p)
			if err != nil {
				panic(err)
			}
			fmt.Println("key=", p, "value=", string(v))
		}
	}

	fmt.Println(m.ObjectPropExists("core", "save.data")) // true
}
```

## Crash-Safe Save Slots

A plain `SaveObjectProp` overwrites the destination in place: if the process
is killed or power is lost while the write is in progress, both the new and
the old data can be gone. Opt into **safe save slots** via `Config.Slots`
to get atomic, validated, versioned saves with automatic rollback:

```go
m, err := gdata.Open(gdata.Config{
    AppName: "mygame",
    Slots:   3, // keep the 3 most recent complete saves per property
})
if err != nil {
    panic(err)
}

// Every save goes into a brand-new slot (frame with checksum + version
// metadata) and is atomically promoted. An interrupted write can never
// touch the previously committed save.
if err := m.SaveSlot("core", "save.data", "build-1042", saveBytes); err != nil {
    panic(err)
}

// LoadLatestSlot validates checksums and transparently rolls back to an
// older slot when the newest one is damaged.
data, info, err := m.LoadLatestSlot("core", "save.data")
if errors.Is(err, gdata.ErrNoSlots) {
    // No readable save exists yet: start a new game.
}

// Let the player pick an older save explicitly.
slots, _ := m.ListSlots("core", "save.data")
for _, s := range slots {
    fmt.Println(s.Sequence, s.Version, s.SavedAt)
}
oldData, _, err := m.LoadSlotVersion("core", "save.data", slots[len(slots)-1].Sequence)
```

How it works, identically on desktop, Android and browser targets:

* each slot is a self-contained frame: magic, frame version, monotonically
  increasing sequence, user version string, SHA-256 checksum and payload;
* the slot is written completely first, then a JSON manifest is atomically
  replaced (temp-file + `rename` + directory fsync on filesystems, a single
  `localStorage.setItem` on wasm) — that replacement is the only commit point;
* `LoadLatestSlot` walks slots newest-first and skips anything that fails
  validation, so corruption never blocks an older good save;
* a damaged manifest is rebuilt automatically from the surviving slot frames;
* `Config.Slots` bounds how many complete saves are retained (older ones are
  pruned after a successful commit);
* the slot-aware API returns typed errors: `ErrSlotsDisabled`, `ErrInvalidKey`
  (path traversal), `ErrNoSlots`, `ErrCorrupted`, `ErrNoSpace`, `ErrBusy`;
* the legacy API (`SaveObjectProp`, `LoadObjectProp`, ...) keeps working
  unchanged, and slots live in an internal `.gslots` directory that never
  appears in `ListObjectProps` results.

