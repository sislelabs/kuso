package handlers

import (
	"context"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

// The env-var masking gate is admin-only (secrets:read). GetService long
// had the mask; ListServices/Describe/ListEnvironments/GetEnvironment and
// the editor-gated mutators (PatchService/AddDomain/SetEnvVar/
// AddEnvironment/…) did NOT — they serialized full CRs with plaintext
// secret values to any editor/viewer. These tests exercise the shared
// mask helpers every serialization site now routes through, proving the
// gate applies uniformly.
//
// Both branches are DB-free:
//   - admin: callerCanReadSecrets short-circuits true via the
//     settings:admin bypass in callerHasProjectPerm (no DB touched).
//   - non-admin + nil DB: callerHasProjectPerm fails closed → false →
//     values get masked.

func maskAdminCtx() context.Context {
	return auth.WithClaimsForTest(context.Background(),
		&auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}})
}

func maskViewerCtx() context.Context {
	// No settings:admin, no per-project grant, nil DB below → the gate
	// fails closed and masks. Models a viewer/editor without secrets:read.
	return auth.WithClaimsForTest(context.Background(),
		&auth.Claims{UserID: "viewer", Permissions: []string{}})
}

func svcWithSecret() *kube.KusoService {
	return &kube.KusoService{
		Spec: kube.KusoServiceSpec{
			EnvVars: []kube.KusoEnvVar{
				{Name: "API_KEY", Value: "super-secret"},
				{Name: "DB_URL", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "x", "key": "y"}}},
			},
		},
	}
}

func envWithSecret() *kube.KusoEnvironment {
	return &kube.KusoEnvironment{
		Spec: kube.KusoEnvironmentSpec{
			EnvVars: []kube.KusoEnvVar{
				{Name: "API_KEY", Value: "super-secret"},
			},
		},
	}
}

func TestMaskServiceEnvIfNeeded_AdminSeesPlaintext(t *testing.T) {
	t.Parallel()
	svc := svcWithSecret()
	maskServiceEnvIfNeeded(maskAdminCtx(), nil, "alpha", svc)
	if svc.Spec.EnvVars[0].Value != "super-secret" {
		t.Errorf("admin should see plaintext, got %q", svc.Spec.EnvVars[0].Value)
	}
}

func TestMaskServiceEnvIfNeeded_ViewerMasked(t *testing.T) {
	t.Parallel()
	svc := svcWithSecret()
	// nil DB + non-admin → fail closed → masked.
	maskServiceEnvIfNeeded(maskViewerCtx(), nil, "alpha", svc)
	if svc.Spec.EnvVars[0].Value != envMaskSentinel {
		t.Errorf("viewer value not masked: got %q want %q", svc.Spec.EnvVars[0].Value, envMaskSentinel)
	}
	// A secretKeyRef entry has no literal Value — it must round-trip
	// untouched so the editor still knows the key exists.
	if svc.Spec.EnvVars[1].Value != "" || svc.Spec.EnvVars[1].ValueFrom == nil {
		t.Errorf("secretKeyRef entry mangled: %+v", svc.Spec.EnvVars[1])
	}
}

func TestMaskServicesEnvIfNeeded_ListMasksAll(t *testing.T) {
	t.Parallel()
	list := []kube.KusoService{*svcWithSecret(), *svcWithSecret()}
	maskServicesEnvIfNeeded(maskViewerCtx(), nil, "alpha", list)
	for i := range list {
		if list[i].Spec.EnvVars[0].Value != envMaskSentinel {
			t.Errorf("service[%d] value not masked in list: %q", i, list[i].Spec.EnvVars[0].Value)
		}
	}

	admin := []kube.KusoService{*svcWithSecret()}
	maskServicesEnvIfNeeded(maskAdminCtx(), nil, "alpha", admin)
	if admin[0].Spec.EnvVars[0].Value != "super-secret" {
		t.Errorf("admin list should stay plaintext, got %q", admin[0].Spec.EnvVars[0].Value)
	}
}

func TestMaskEnvIfNeeded_ViewerMaskedAdminPlaintext(t *testing.T) {
	t.Parallel()
	env := envWithSecret()
	maskEnvIfNeeded(maskViewerCtx(), nil, "alpha", env)
	if env.Spec.EnvVars[0].Value != envMaskSentinel {
		t.Errorf("viewer env value not masked: got %q", env.Spec.EnvVars[0].Value)
	}

	env2 := envWithSecret()
	maskEnvIfNeeded(maskAdminCtx(), nil, "alpha", env2)
	if env2.Spec.EnvVars[0].Value != "super-secret" {
		t.Errorf("admin env value should be plaintext, got %q", env2.Spec.EnvVars[0].Value)
	}
}

func TestMaskEnvsIfNeeded_ListMasksAll(t *testing.T) {
	t.Parallel()
	list := []kube.KusoEnvironment{*envWithSecret(), *envWithSecret()}
	maskEnvsIfNeeded(maskViewerCtx(), nil, "alpha", list)
	for i := range list {
		if list[i].Spec.EnvVars[0].Value != envMaskSentinel {
			t.Errorf("env[%d] value not masked in list: %q", i, list[i].Spec.EnvVars[0].Value)
		}
	}
}

// Nil inputs must be no-ops (handlers may hand a nil CR on some paths).
func TestMaskHelpers_NilSafe(t *testing.T) {
	t.Parallel()
	maskServiceEnvIfNeeded(maskViewerCtx(), nil, "alpha", nil)
	maskEnvIfNeeded(maskViewerCtx(), nil, "alpha", nil)
	maskServicesEnvIfNeeded(maskViewerCtx(), nil, "alpha", nil)
	maskEnvsIfNeeded(maskViewerCtx(), nil, "alpha", nil)
}
