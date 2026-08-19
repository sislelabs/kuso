package kusoCli

import "testing"

// The top-level `kuso service …` alias must expose EVERY subcommand of
// `kuso project service …` — a partial alias is a trap (`kuso service
// stop` used to error for a command that exists one level over).
func TestServiceAlias_ExposesAllProjectServiceSubcommands(t *testing.T) {
	have := map[string]bool{}
	for _, c := range serviceCmd.Commands() {
		have[c.Name()] = true
	}
	for _, c := range projectServiceCmd.Commands() {
		if !have[c.Name()] {
			t.Errorf("`kuso service %s` missing — `kuso project service %s` exists; mirror it via aliasOf in service_toplevel_alias.go", c.Name(), c.Name())
		}
	}
}

// Guarded originals must keep their --yes flag through the alias.
func TestServiceAlias_GuardedSubcommandsKeepYesFlag(t *testing.T) {
	for _, name := range []string{"delete", "rename", "stop"} {
		found := false
		for _, c := range serviceCmd.Commands() {
			if c.Name() == name {
				found = true
				if c.Flags().Lookup("yes") == nil {
					t.Errorf("`kuso service %s`: --yes flag lost in the alias shell", name)
				}
			}
		}
		if !found {
			t.Errorf("`kuso service %s` not registered", name)
		}
	}
}
