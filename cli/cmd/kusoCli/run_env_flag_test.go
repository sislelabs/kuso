package kusoCli

import (
	"reflect"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestRunSetEnvFlagParsing(t *testing.T) {
	t.Cleanup(func() {
		runSetEnvFlags, runEnvFlags = nil, nil
		for _, n := range []string{"set-env", "env"} {
			runCmd.Flags().Lookup(n).Changed = false
		}
	})
	err := runCmd.Flags().Parse([]string{"-e", "A=1", "--set-env", "B=2", "--env", "C=3"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseRunEnvFlags(append(append([]string{}, runSetEnvFlags...), runEnvFlags...))
	if err != nil {
		t.Fatal(err)
	}
	want := []kusoApi.RunEnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}, {Name: "C", Value: "3"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if runCmd.Flags().Lookup("env").Deprecated == "" {
		t.Error("--env should be marked deprecated")
	}
}

func TestRunSetEnvRejectsBarePair(t *testing.T) {
	if _, err := parseRunEnvFlags([]string{"staging"}); err == nil {
		t.Error("want KEY=VALUE error for a bare word")
	}
}
