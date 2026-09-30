package kusoCli

import "testing"

func TestPreviousBuild(t *testing.T) {
	t.Parallel()
	items := []buildRow{ // newest first
		{ID: "b5", Branch: "main", Status: "failed"},
		{ID: "b4", Branch: "main", Status: "succeeded", ImageTag: "t4", LiveEnvs: []string{"production"}},
		{ID: "b3", Branch: "feature", Status: "succeeded", ImageTag: "t3", LiveEnvs: []string{"preview-pr-2"}},
		{ID: "b2", Branch: "main", Status: "succeeded"}, // archived, image pruned
		{ID: "b1", Branch: "main", Status: "succeeded", ImageTag: "t1"},
	}
	got, err := previousBuild(items, "production")
	if err != nil || got != "b1" {
		t.Errorf("production: got %q, %v; want b1", got, err)
	}
	if _, err := previousBuild(items, "preview-pr-2"); err == nil {
		t.Error("preview-pr-2 has no earlier feature build; want an error")
	}
	if _, err := previousBuild(items, "staging"); err == nil {
		t.Error("nothing live on staging; want an error")
	}
}
