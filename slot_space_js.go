//go:build js

package gdata

import "errors"

// errLocalStorageQuota is returned by jsSlotStore.SlotWrite when the browser
// rejects a write because the localStorage quota is exhausted.
var errLocalStorageQuota = errors.New("localStorage quota exceeded")

func init() {
	noSpaceHooks = append(noSpaceHooks, func(err error) bool {
		return errors.Is(err, errLocalStorageQuota)
	})
}
