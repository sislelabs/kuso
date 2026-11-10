package addons

import (
	"os"
	"regexp"
	"testing"
)

// pgsty/mc has no grep, awk or sed: a script that pipes into one of them
// fails at runtime (v0.42.1 broke S3 rotation and new-addon init this way).
var mcMissingTool = regexp.MustCompile(`\|\s*(grep|awk|sed)\b`)

func TestS3AdminScriptsUseOnlyShellBuiltins(t *testing.T) {
	if mcMissingTool.MatchString(s3RotateScript) {
		t.Error("rotate script pipes into grep/awk/sed, which pgsty/mc lacks")
	}
	chart, err := os.ReadFile("../../../operator/helm-charts/kusoaddon/templates/s3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if loc := mcMissingTool.FindIndex(chart); loc != nil {
		t.Errorf("s3.yaml pipes into a tool pgsty/mc lacks: %q", chart[loc[0]:loc[1]])
	}
}
