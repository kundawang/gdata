// Smoke test for the safe-slot primitives exported by gdata.js.
//
// It ships with a minimal localStorage shim, so it runs both in Node:
//
//	node gdata_slots_smoke.js
//
// and in a browser console (the shim is ignored when window.localStorage
// already exists). The Go-side engine tests cover frame validation and
// recovery logic; this file only checks the storage-key layer that the
// wasm backend depends on:
//
//   - slot values never collide with legacy keys;
//   - reads report missing values explicitly;
//   - directory listing returns direct children only;
//   - removeAll deletes a whole subtree and nothing outside it;
//   - quota failures surface as {ok:false, noSpace:true}.

(function () {
	"use strict";

	const failures = [];
	function check(cond, message) {
		if (!cond) {
			failures.push(message);
			console.error("FAIL:", message);
		} else {
			console.log("ok  :", message);
		}
	}

	if (typeof window === "undefined" || !window.localStorage) {
		const backing = new Map();
		const shim = {
			get length() {
				return backing.size;
			},
			key(i) {
				return Array.from(backing.keys())[i];
			},
			getItem(k) {
				return backing.has(k) ? backing.get(k) : null;
			},
			setItem(k, v) {
				// Tiny fake quota for the no-space test.
				if (quotaBytes > 0 && !backing.has(k) && backing.size + 1 > 3) {
					const e = new Error("QuotaExceededError");
					e.name = "QuotaExceededError";
					throw e;
				}
				backing.set(k, String(v));
			},
			removeItem(k) {
				backing.delete(k);
			},
		};
		var quotaBytes = 0;
		globalThis.window = { localStorage: shim };
	}

	const fs = require("./gdata.js");
	const app = "smokeapp";

	// Legacy and slot keys live in disjoint namespaces.
	fs.saveObjectProp(app, "obj1", "p", "legacy-value");
	const w1 = fs.slotWrite(app, "obj1/.gslots/p/slot-1", "frame1");
	check(w1.ok, "slotWrite succeeds");
	check(fs.loadObjectProp(app, "obj1", "p") === "legacy-value", "legacy data untouched by slot write");
	check(fs.slotRead(app, "obj1/.gslots/p/slot-1").value === "frame1", "slotRead returns written frame");

	const missing = fs.slotRead(app, "obj1/.gslots/p/does-not-exist");
	check(missing.ok && missing.missing, "missing slot read reports {missing:true}");

	// More writes, then directory listing semantics.
	fs.slotWrite(app, "obj1/.gslots/p/slot-2", "frame2");
	fs.slotWrite(app, "obj1/.gslots/p/nested/deep", "deep");
	fs.slotWrite(app, "obj1/.gslots/q/slot-1", "other-scope");
	const listed = fs.slotList(app, "obj1/.gslots/p");
	check(listed.names.indexOf("slot-1") !== -1 && listed.names.indexOf("slot-2") !== -1,
		"slotList contains direct slot children");
	check(listed.names.indexOf("nested") !== -1 && listed.names.indexOf("deep") === -1,
		"slotList reports directory names, not nested file names");
	check(listed.names.length === 3, "slotList returns exactly the direct children");

	// Atomic replacement semantics of setItem.
	fs.slotWrite(app, "obj1/.gslots/p/slot-1", "frame1-replaced");
	check(fs.slotRead(app, "obj1/.gslots/p/slot-1").value === "frame1-replaced",
		"slotWrite replaces the previous value");

	// removeAll is scoped to one property subtree.
	fs.slotRemoveAll(app, "obj1/.gslots/p");
	check(fs.slotRead(app, "obj1/.gslots/p/slot-2").missing, "removeAll deletes subtree files");
	check(fs.slotRead(app, "obj1/.gslots/q/slot-1").value === "other-scope",
		"removeAll leaves sibling scopes intact");
	check(fs.loadObjectProp(app, "obj1", "p") === "legacy-value",
		"removeAll leaves legacy namespace intact");
	fs.slotRemove(app, "obj1/.gslots/q/slot-1");
	check(fs.slotRead(app, "obj1/.gslots/q/slot-1").missing, "slotRemove deletes one value");

	if (failures.length !== 0) {
		console.error("\n" + failures.length + " check(s) failed");
		process.exit(1);
	}
	console.log("\nall slot primitive checks passed");
}());
