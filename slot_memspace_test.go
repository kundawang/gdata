package gdata

// Test hook: make the injected "disk full" error recognizable as ErrNoSpace.
// On real platforms the mapping in slot_space_*.go handles OS errno values.
func init() {
	noSpaceHooks = append(noSpaceHooks, func(err error) bool {
		return err == errInjectedNoSpace
	})
}
