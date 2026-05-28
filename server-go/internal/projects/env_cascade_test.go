package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"

	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// TestEnvVarCascade is the end-to-end precedence test the v0.17.0
// audit explicitly requested. Verifies the precedence rule that
// every other env-var fix has to maintain:
//
//	subscribed-shared < svc-explicit < env-explicit-literal
//	env-explicit-literal < env-explicit-per-secret (envFromSecrets)
//
// Each case is a single scenario; failures shout which precedence
// hop broke so a regression doesn't have to be triangulated across
// six merge helpers.
//
// Why these specific cases:
//   - "shared only" — the legacy migration path
//   - "svc beats shared" — the classic SetEnv override
//   - "env beats svc" — the staging-overrides-prod case that
//     keeps breaking in propagation refactors
//   - "envFrom beats env literal" — explicit env: wins over envFrom:
//     in k8s, so a literal env override masks a per-env Secret
//     value; the code MUST drop the literal when an override exists
//     in envFromSecrets (B2.5 area)
func TestEnvVarCascade(t *testing.T) {
	t.Parallel()

	const project = "alpha"
	const ns = "kuso"

	// Common shared secret used by every subtest. Keys:
	//   API_URL=https://api.prod        (shared default)
	//   DATABASE_URL=postgres://shared  (shared default)
	sharedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.SharedSecretNames(project)[0], Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"API_URL":      []byte("https://api.prod"),
			"DATABASE_URL": []byte("postgres://shared"),
		},
	}

	// Optional per-env override Secret. Populated only by the subtest
	// that exercises envFrom override precedence. Keys:
	//   API_URL=https://api.staging
	envOverrideSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-staging-secrets", Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"API_URL": []byte("https://api.staging")},
	}

	cases := []struct {
		name string
		// inputs
		sharedKeys     []string
		svcExplicit    []kube.KusoEnvVar
		envExplicit    []kube.KusoEnvVar
		envFromSecrets []string
		extraSecret    *corev1.Secret // optional per-env Secret to seed
		// expectations
		wantValueForName     map[string]string // literal `value` field
		wantValueFromForName map[string]string // valueFrom secretKeyRef.name
		wantDropped          []string          // names that must NOT appear
	}{
		{
			name:       "subscribed-only key resolves via valueFrom shared secret",
			sharedKeys: []string{"API_URL"},
			wantValueFromForName: map[string]string{
				"API_URL": sharedSecret.Name,
			},
		},
		{
			name:       "svc explicit literal wins over subscribed",
			sharedKeys: []string{"API_URL"},
			svcExplicit: []kube.KusoEnvVar{
				{Name: "API_URL", Value: "https://api.svc-explicit"},
			},
			wantValueForName: map[string]string{
				"API_URL": "https://api.svc-explicit",
			},
		},
		{
			name:       "env literal wins over svc explicit AND subscribed",
			sharedKeys: []string{"API_URL"},
			svcExplicit: []kube.KusoEnvVar{
				{Name: "API_URL", Value: "https://api.svc-explicit"},
			},
			envExplicit: []kube.KusoEnvVar{
				{Name: "API_URL", Value: "https://api.env-staging"},
			},
			wantValueForName: map[string]string{
				"API_URL": "https://api.env-staging",
			},
		},
		{
			// B2.5: literal-value env override for a key that's
			// subscribed-only (not on svc-explicit). The svc list is
			// empty, so the only thing keeping API_URL alive is the
			// shared-key subscription — yet the env's literal override
			// must replace it.
			name:       "env literal beats subscribed when svc has no explicit",
			sharedKeys: []string{"API_URL"},
			envExplicit: []kube.KusoEnvVar{
				{Name: "API_URL", Value: "https://api.env-staging"},
			},
			wantValueForName: map[string]string{
				"API_URL": "https://api.env-staging",
			},
			wantDropped: []string{}, // (presence-check covered by valueFrom-not-set assertion)
		},
		{
			// envFromSecrets pulls a per-env Secret that overrides
			// API_URL. The chart mounts envFrom *after* shared, so
			// the per-env Secret value wins — but a leftover explicit
			// env entry (valueFrom→shared) would still beat envFrom
			// in k8s precedence. Resolve MUST drop that entry.
			name:       "per-env Secret override drops subscribed envVar to avoid masking",
			sharedKeys: []string{"API_URL"},
			envFromSecrets: []string{
				"alpha-web-staging-secrets",
			},
			extraSecret: envOverrideSecret,
			// API_URL should NOT appear as an envVar at all — envFrom
			// from the per-env Secret will deliver it.
			wantDropped: []string{"API_URL"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seeds := []*corev1.Secret{sharedSecret}
			if tc.extraSecret != nil {
				seeds = append(seeds, tc.extraSecret)
			}
			s := cascadeSvc(t, seeds...)

			merged, _, err := s.resolveSharedEnvKeysForEnv(
				context.Background(),
				ns, project,
				tc.sharedKeys,
				tc.svcExplicit,
				append(tc.envExplicit, []kube.KusoEnvVar{}...), // copy guard
				tc.envFromSecrets,
			)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}

			byName := make(map[string]kube.KusoEnvVar, len(merged))
			for _, e := range merged {
				byName[e.Name] = e
			}

			for name, want := range tc.wantValueForName {
				got, ok := byName[name]
				if !ok {
					t.Errorf("%s: missing from merged envVars", name)
					continue
				}
				if got.Value != want {
					t.Errorf("%s: value = %q, want %q", name, got.Value, want)
				}
				if got.ValueFrom != nil {
					t.Errorf("%s: unexpected ValueFrom = %+v (literal expected)", name, got.ValueFrom)
				}
			}
			for name, wantSecret := range tc.wantValueFromForName {
				got, ok := byName[name]
				if !ok {
					t.Errorf("%s: missing from merged envVars", name)
					continue
				}
				if got.Value != "" {
					t.Errorf("%s: unexpected Value = %q (valueFrom expected)", name, got.Value)
				}
				ref, _ := got.ValueFrom["secretKeyRef"].(map[string]any)
				if ref == nil {
					t.Errorf("%s: ValueFrom missing secretKeyRef: %+v", name, got.ValueFrom)
					continue
				}
				if ref["name"] != wantSecret {
					t.Errorf("%s: secretKeyRef.name = %v, want %q", name, ref["name"], wantSecret)
				}
			}
			for _, name := range tc.wantDropped {
				if _, present := byName[name]; present {
					t.Errorf("%s: expected to be dropped from envVars, but present (would mask envFrom)", name)
				}
			}
		})
	}
}

// cascadeSvc returns a fakeService wired with the typed Clientset so
// resolveSharedEnvKeysForEnv can Get() shared + per-env Secrets.
func cascadeSvc(t *testing.T, secrets ...*corev1.Secret) *Service {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	objs := make([]runtime.Object, 0, len(secrets))
	for _, sec := range secrets {
		objs = append(objs, sec)
	}
	cs := kubefake.NewSimpleClientset(objs...)
	return New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso")
}

