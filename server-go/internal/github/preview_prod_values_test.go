package github

import (
	"context"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// ISO-1: an env-group staging starts as a verbatim copy of production's
// service Secret. A preview based on it gets none of the values that are
// still production's, whichever Secret or env var carries them; keys staging
// overrode come through, and the withheld keys are reported.
func TestDispatch_PROpened_WithholdsProductionValuesFromEnvGroupBase(t *testing.T) {
	t.Parallel()
	group := seedEnvCR(&kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "alpha-api-staging-production",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api-staging", kube.LabelEnv: "staging"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api-staging", Kind: "production",
			EnvFromSecrets: []string{"alpha-api-staging-secrets", "alpha-api-staging-staging-secrets"},
			EnvVars: []kube.KusoEnvVar{
				{Name: "PAY_KEY", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-api-staging-secrets", "key": "STRIPE_KEY"}}},
				{Name: "MAIL_TOKEN", Value: "mt_live"},
				{Name: "MODE", Value: "test"},
			},
		},
	})
	d := newDispatcherNoStaging(t, seedProj("alpha", "https://github.com/example/alpha", "main", true, 5), seedSvc("alpha", "api"), prodEnvWithSecret(), group)
	rec := &recordingEmitter{}
	d.Notifier = rec
	seedSecret(t, d, "alpha-api-secrets", map[string]string{"STRIPE_KEY": "sk_live", "JWT": "jwt_live", "MAIL_TOKEN": "mt_live", "EMPTY": ""})
	seedSecret(t, d, "alpha-api-production-secrets", map[string]string{"WEBHOOK": "wh_live"})
	seedSecret(t, d, "alpha-api-staging-secrets", map[string]string{"STRIPE_KEY": "sk_live", "JWT": "jwt_staging", "EMPTY": ""})
	seedSecret(t, d, "alpha-api-staging-staging-secrets", map[string]string{"WEBHOOK": "wh_live", "OWN": "x"})

	env := dispatchPR7(t, d)
	pr, err := d.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-api-pr-7-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pr secret: %v", err)
	}
	for _, k := range []string{"STRIPE_KEY", "WEBHOOK"} {
		if _, leaked := pr.Data[k]; leaked {
			t.Errorf("production value of %s copied into the preview: %v", k, pr.Data)
		}
	}
	if string(pr.Data["JWT"]) != "jwt_staging" || string(pr.Data["OWN"]) != "x" {
		t.Errorf("staging's own values missing from the preview: %v", pr.Data)
	}
	if _, ok := pr.Data["EMPTY"]; !ok {
		t.Errorf("an empty value is not production's secret, keep it: %v", pr.Data)
	}
	var names []string
	for _, v := range env.Spec.EnvVars {
		names = append(names, v.Name)
	}
	if slices.Contains(names, "PAY_KEY") || slices.Contains(names, "MAIL_TOKEN") || !slices.Contains(names, "MODE") {
		t.Errorf("preview env vars = %v, want MODE only", names)
	}
	if len(rec.events) != 1 {
		t.Fatalf("withheld keys not reported once: %+v", rec.events)
	}
	for _, k := range []string{"STRIPE_KEY", "WEBHOOK", "PAY_KEY", "MAIL_TOKEN"} {
		if !strings.Contains(rec.events[0].Body, k) {
			t.Errorf("report does not name %s: %s", k, rec.events[0].Body)
		}
	}
}
