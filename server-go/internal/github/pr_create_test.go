package github

import (
	"context"
	"testing"
)

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
	if _, err := c.CreateBranch(context.Background(), 1, "o", "r", "main", "fix"); err == nil {
		t.Error("CreateBranch on nil client: want error, got nil")
	}
	if _, _, err := c.OpenPR(context.Background(), 1, "o", "r", "fix", "main", "t", "b"); err == nil {
		t.Error("OpenPR on nil client: want error, got nil")
	}
	if err := c.MergePR(context.Background(), 1, "o", "r", 1, "squash"); err == nil {
		t.Error("MergePR on nil client: want error, got nil")
	}
}
