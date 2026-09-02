package kube

import (
	"reflect"
	"testing"
)

// Kubernetes envFrom is last-source-wins on duplicate keys. Every postgres
// addon publishes the same keys (DATABASE_URL, DIRECT_URL, POSTGRES_*), so
// when an env mounts both its OWN clone (tickero-db-staging-conn) and a
// project-level addon (tickero-psdb-conn), whichever is listed last decides
// which database the pod talks to. The list was alphabetical, so
// "psdb" beat "db-staging" by spelling — and tickero-api-staging ran its
// migration release hook against production's DIRECT_URL.
//
// The env's own clone was minted FOR it; it must win. Order clones last.
func TestCloneConnsLast(t *testing.T) {
	cases := []struct {
		name  string
		scope string
		in    []string
		want  []string
	}{
		{
			name:  "named env: own clone moves after a project-level addon of the same kind",
			scope: "staging",
			in:    []string{"tickero-cache-staging-conn", "tickero-db-staging-conn", "tickero-api-secrets", "tickero-psdb-conn"},
			want:  []string{"tickero-api-secrets", "tickero-psdb-conn", "tickero-cache-staging-conn", "tickero-db-staging-conn"},
		},
		{
			name:  "preview env: clone is named -pr-N although the scope is preview-pr-N",
			scope: "preview-pr-52",
			in:    []string{"tickero-db-pr-52-conn", "tickero-psdb-conn", "tickero-api-preview-pr-52-secrets"},
			want:  []string{"tickero-psdb-conn", "tickero-api-preview-pr-52-secrets", "tickero-db-pr-52-conn"},
		},
		{
			name:  "production has no clones; order untouched",
			scope: "production",
			in:    []string{"tickero-cache-conn", "tickero-psdb-conn", "tickero-api-secrets"},
			want:  []string{"tickero-cache-conn", "tickero-psdb-conn", "tickero-api-secrets"},
		},
		{
			name:  "relative order among clones and among non-clones is stable",
			scope: "staging",
			in:    []string{"b-staging-conn", "z-conn", "a-staging-conn", "y-conn"},
			want:  []string{"z-conn", "y-conn", "b-staging-conn", "a-staging-conn"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CloneConnsLast(tc.in, tc.scope)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CloneConnsLast(%v, %q)\n got %v\nwant %v", tc.in, tc.scope, got, tc.want)
			}
		})
	}
}

func TestIsEnvScopedConn(t *testing.T) {
	if !IsEnvScopedConn("tickero-db-staging-conn", "staging") {
		t.Error("named-env clone not recognised")
	}
	if !IsEnvScopedConn("tickero-db-pr-52-conn", "preview-pr-52") {
		t.Error("preview clone (-pr-N name, preview-pr-N scope) not recognised")
	}
	if IsEnvScopedConn("tickero-psdb-conn", "staging") {
		t.Error("project-level conn misclassified as a clone")
	}
	if IsEnvScopedConn("tickero-api-staging-secrets", "staging") {
		t.Error("a per-env managed secret is not an addon conn")
	}
	if IsEnvScopedConn("tickero-db-staging-conn", "production") {
		t.Error("another env's clone is not this env's")
	}
}
