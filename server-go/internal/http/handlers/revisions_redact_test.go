package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"kuso/server/internal/db"
)

// The revision snapshot is the RAW patch body — the History endpoints
// were a viewer-readable side door around every read gate (env masks,
// repo-URL redaction, and the GitLab token that was never supposed to
// be persisted at all). These pin the scrubber against each secret
// shape current snapshots can carry.

func TestRedactRevisionSnapshot_ViewerScrubbed(t *testing.T) {
	t.Parallel()
	snap := []byte(`{"patch":{
		"repo":{"url":"https://kuso-deploy:gldt-FAKE@gitlab.com/o/r.git","branch":"main","token":"glpat-FAKEFAKE"},
		"envVars":[{"name":"API_KEY","value":"super-secret"},{"name":"PLAIN","value":"ok"}],
		"password":"hunter2",
		"port":3000}}`)
	rev := &db.Revision{Project: "alpha", Snapshot: snap}
	// maskViewerCtx + nil DB → callerCanReadSecrets fails closed.
	redactRevisionSnapshotIfNeeded(maskViewerCtx(), nil, "alpha", rev)

	out := string(rev.Snapshot)
	for _, leaked := range []string{"gldt-FAKE", "glpat-FAKEFAKE", "super-secret", "hunter2"} {
		if strings.Contains(out, leaked) {
			t.Errorf("secret %q survived redaction: %s", leaked, out)
		}
	}
	// Non-secret structure must survive so the History diff still renders.
	var decoded struct {
		Patch struct {
			Repo struct {
				URL    string `json:"url"`
				Branch string `json:"branch"`
			} `json:"repo"`
			Port int `json:"port"`
		} `json:"patch"`
	}
	if err := json.Unmarshal(rev.Snapshot, &decoded); err != nil {
		t.Fatalf("redacted snapshot no longer parses: %v", err)
	}
	if decoded.Patch.Repo.URL != "https://gitlab.com/o/r.git" {
		t.Errorf("repo URL should be stripped, not destroyed: %q", decoded.Patch.Repo.URL)
	}
	if decoded.Patch.Repo.Branch != "main" || decoded.Patch.Port != 3000 {
		t.Errorf("non-secret fields mangled: %+v", decoded.Patch)
	}
}

func TestRedactRevisionSnapshot_AdminUntouched(t *testing.T) {
	t.Parallel()
	snap := []byte(`{"patch":{"repo":{"token":"glpat-FAKEFAKE"}}}`)
	rev := &db.Revision{Project: "alpha", Snapshot: append([]byte(nil), snap...)}
	redactRevisionSnapshotIfNeeded(maskAdminCtx(), nil, "alpha", rev)
	if string(rev.Snapshot) != string(snap) {
		t.Errorf("admin snapshot modified: %s", rev.Snapshot)
	}
}

func TestRedactRevisionSnapshot_UnparseableFailsClosed(t *testing.T) {
	t.Parallel()
	rev := &db.Revision{Project: "alpha", Snapshot: []byte(`{"broken`)}
	redactRevisionSnapshotIfNeeded(maskViewerCtx(), nil, "alpha", rev)
	if string(rev.Snapshot) != "{}" {
		t.Errorf("unparseable snapshot must collapse to {}, got %s", rev.Snapshot)
	}
}
