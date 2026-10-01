package kusoCli

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func TestPositionalSlots(t *testing.T) {
	cases := map[string][]string{
		"sql <project> <addon> <query>":                {"project", "addon", "query"},
		"apply [file]":                                 {"file"},
		"tables <project> <addon> [--schema <schema>]": {"project", "addon"},
		"list <project> [<service>]":                   {"project", "service"},
		"compose <docker-compose.yml>":                 {"docker-compose.yml"},
		"cleanup":                                      nil,
	}
	for use, want := range cases {
		if got := positionalSlots(use); !reflect.DeepEqual(got, want) {
			t.Errorf("positionalSlots(%q) = %v, want %v", use, got, want)
		}
	}
}

// `kuso apply <TAB>` used to suggest project names and turn file
// completion off, though the argument is a file.
func TestCompleteFromUse_FileSlotKeepsFileCompletion(t *testing.T) {
	_, dir := completeFromUse(applyCmd, nil, "")
	if dir != cobra.ShellCompDirectiveDefault {
		t.Errorf("apply [file]: directive = %v, want default (file completion)", dir)
	}
}
