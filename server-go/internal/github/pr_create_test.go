package github

import "testing"

func TestParseRepoFullName(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{name: "simple", in: "sislelabs/kuso", wantOwner: "sislelabs", wantRepo: "kuso"},
		{name: "leading slash", in: "/sislelabs/kuso", wantOwner: "sislelabs", wantRepo: "kuso"},
		{name: "trailing slash", in: "sislelabs/kuso/", wantOwner: "sislelabs", wantRepo: "kuso"},
		{name: "surrounding whitespace", in: "  sislelabs/kuso  ", wantOwner: "sislelabs", wantRepo: "kuso"},
		{name: "no slash", in: "kuso", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "just slash", in: "/", wantErr: true},
		{name: "empty owner", in: "/kuso", wantErr: true},
		{name: "empty repo", in: "sislelabs/", wantErr: true},
		{name: "too many segments", in: "sislelabs/kuso/extra", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := parseRepoFullName(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRepoFullName(%q) = (%q,%q,nil), want error", tt.in, owner, repo)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRepoFullName(%q) unexpected error: %v", tt.in, err)
			}
			if owner != tt.wantOwner || repo != tt.wantRepo {
				t.Errorf("parseRepoFullName(%q) = (%q,%q), want (%q,%q)", tt.in, owner, repo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func TestNormalizeMergeMethod(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"squash", "squash"},
		{"merge", "merge"},
		{"rebase", "rebase"},
		{"", "squash"},
		{"SQUASH", "squash"},
		{"  Merge  ", "merge"},
		{"REBASE", "rebase"},
		{"nonsense", "squash"},
		{"fast-forward", "squash"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := normalizeMergeMethod(tt.in); got != tt.want {
				t.Errorf("normalizeMergeMethod(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestPRMethods_NilClientGuard ensures the exported methods fail closed
// (no panic) when invoked on a nil *Client — the not-configured case the
// rest of the package guards the same way (see PostPRComment).
func TestPRMethods_NilClientGuard(t *testing.T) {
	var c *Client
	if _, err := c.CreateBranch(nil, 1, "o", "r", "main", "fix"); err == nil {
		t.Error("CreateBranch on nil client: want error, got nil")
	}
	if _, _, err := c.OpenPR(nil, 1, "o", "r", "fix", "main", "t", "b"); err == nil {
		t.Error("OpenPR on nil client: want error, got nil")
	}
	if err := c.MergePR(nil, 1, "o", "r", 1, "squash"); err == nil {
		t.Error("MergePR on nil client: want error, got nil")
	}
}
