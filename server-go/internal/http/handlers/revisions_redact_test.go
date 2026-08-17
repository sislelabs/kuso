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

// buildArgs / buildEnv are KEY→VALUE maps with user-chosen keys, so the
// per-key "password"/"token" rules can't reach them — yet they're the
// conventional home for build-time credentials. A project VIEWER (who
// must not see secret values) can read revisions, so every value has to
// be masked while the keys survive for the History diff.
func TestRedactRevisionSnapshot_BuildArgsMasked(t *testing.T) {
	t.Parallel()
	snap := []byte(`{"patch":{
		"buildArgs":{"NPM_TOKEN":"npm_FAKESECRET","SENTRY_AUTH_TOKEN":"sntrys_FAKE","PUBLIC_FLAG":"true"},
		"buildEnv":{"PRIVATE_KEY":"-----BEGIN KEY-----abc"},
		"port":8080}}`)
	rev := &db.Revision{Project: "alpha", Snapshot: snap}
	redactRevisionSnapshotIfNeeded(maskViewerCtx(), nil, "alpha", rev)

	out := string(rev.Snapshot)
	for _, leaked := range []string{"npm_FAKESECRET", "sntrys_FAKE", "-----BEGIN KEY-----abc"} {
		if strings.Contains(out, leaked) {
			t.Errorf("build secret %q survived redaction: %s", leaked, out)
		}
	}
	// Even a non-secret-looking value is masked: we can't tell which
	// build args are sensitive, so all values go.
	if strings.Contains(out, `"true"`) {
		t.Errorf("buildArgs values must be masked wholesale: %s", out)
	}

	var decoded struct {
		Patch struct {
			BuildArgs map[string]string `json:"buildArgs"`
			BuildEnv  map[string]string `json:"buildEnv"`
			Port      int               `json:"port"`
		} `json:"patch"`
	}
	if err := json.Unmarshal(rev.Snapshot, &decoded); err != nil {
		t.Fatalf("redacted snapshot no longer parses: %v", err)
	}
	// Keys must survive so History can still show WHICH args changed.
	for _, k := range []string{"NPM_TOKEN", "SENTRY_AUTH_TOKEN", "PUBLIC_FLAG"} {
		if _, ok := decoded.Patch.BuildArgs[k]; !ok {
			t.Errorf("buildArgs key %q was dropped; keys must survive redaction", k)
		}
	}
	if _, ok := decoded.Patch.BuildEnv["PRIVATE_KEY"]; !ok {
		t.Error("buildEnv key was dropped; keys must survive redaction")
	}
	if decoded.Patch.Port != 8080 {
		t.Errorf("non-secret fields mangled: %+v", decoded.Patch)
	}
}
