package handlers

import (
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

// The SQL browser provisions a NOSUPERUSER kuso_browser role and refuses to
// fall back to the addon's own account. That guard exists because the addon
// chart's `kuso` user IS a superuser on the stock postgres image, where a
// browser session could reach COPY … TO PROGRAM.
//
// A managed provider inverts both halves: the credentials kuso holds are an
// ordinary non-superuser (so there is nothing to escalate away from) and they
// lack CREATEROLE (so the role can never be provisioned). Fail-closed there
// means the browser is permanently unavailable for a database that was never
// dangerous. Skip provisioning for external addons; keep it everywhere else.

func TestNeedsBrowserRole_InClusterAddonStillProvisions(t *testing.T) {
	addon := &kube.KusoAddon{Spec: kube.KusoAddonSpec{Kind: "postgres"}}
	if !needsBrowserRole(addon) {
		t.Error("in-cluster postgres must still use the restricted role — its admin user is a superuser")
	}
}

func TestNeedsBrowserRole_ExternalAddonSkips(t *testing.T) {
	addon := &kube.KusoAddon{Spec: kube.KusoAddonSpec{
		Kind:     "postgres",
		External: &kube.KusoAddonExternal{SecretName: "tickero-psdb-external"},
	}}
	if needsBrowserRole(addon) {
		t.Error("external addons cannot CREATE ROLE and hold no superuser; provisioning can only fail")
	}
}

// An external addon whose secret names no source is not external in practice.
func TestNeedsBrowserRole_EmptyExternalSecretStillProvisions(t *testing.T) {
	addon := &kube.KusoAddon{Spec: kube.KusoAddonSpec{
		Kind:     "postgres",
		External: &kube.KusoAddonExternal{},
	}}
	if !needsBrowserRole(addon) {
		t.Error("an external block with no secretName is not an external addon")
	}
}

// Managed providers reject plaintext. The in-cluster addon serves plaintext by
// default, so the sslmode has to follow the addon rather than be hardcoded.
func TestBrowserSSLMode(t *testing.T) {
	inCluster := &kube.KusoAddon{Spec: kube.KusoAddonSpec{Kind: "postgres"}}
	if got := browserSSLMode(inCluster); got != "disable" {
		t.Errorf("in-cluster sslmode = %q, want disable", got)
	}

	tlsAddon := &kube.KusoAddon{Spec: kube.KusoAddonSpec{Kind: "postgres", TLS: "require"}}
	if got := browserSSLMode(tlsAddon); got != "require" {
		t.Errorf("tls=require addon sslmode = %q, want require", got)
	}

	external := &kube.KusoAddon{Spec: kube.KusoAddonSpec{
		Kind:     "postgres",
		External: &kube.KusoAddonExternal{SecretName: "s"},
	}}
	if got := browserSSLMode(external); got != "require" {
		t.Errorf("external sslmode = %q, want require — managed providers refuse plaintext", got)
	}
}

func TestPgOpenAsDSN_CarriesSSLMode(t *testing.T) {
	dsn := pgDSN("h", "5432", "u", "p", "db", "require")
	if !strings.Contains(dsn, "sslmode=require") {
		t.Errorf("dsn = %q, want sslmode=require", dsn)
	}
	if strings.Contains(dsn, "sslmode=disable") {
		t.Errorf("dsn = %q still pins sslmode=disable", dsn)
	}
}
