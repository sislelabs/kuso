package handlers

import (
	"testing"

	"kuso/server/internal/crons"
	"kuso/server/internal/kube"
)

func cronWithWebhook(url string) kube.KusoCron {
	return kube.KusoCron{Spec: kube.KusoCronSpec{
		OnFailure: &kube.KusoCronOnFailure{
			WebhookURL: url,
			SecretRef:  &kube.KusoSecretKeyRef{Name: "p1-hooks", Key: "sig"},
		},
	}}
}

// The failure-webhook URL often embeds a token (Slack/Discord hook
// paths), and the cron List/Get routes are viewer-gated — it must be
// masked for callers without secrets:read, and left intact for admins.
func TestMaskCronsIfNeeded_ViewerMaskedAdminPlaintext(t *testing.T) {
	t.Parallel()
	const hook = "https://hooks.slack.com/services/T0/B0/secret-token"
	in := []kube.KusoCron{cronWithWebhook(hook), {}}

	viewer := maskCronsIfNeeded(maskViewerCtx(), nil, "p1", in)
	if got := viewer[0].Spec.OnFailure.WebhookURL; got != envMaskSentinel {
		t.Errorf("viewer sees webhookURL %q, want mask", got)
	}
	if viewer[0].Spec.OnFailure.SecretRef == nil || viewer[0].Spec.OnFailure.SecretRef.Name != "p1-hooks" {
		t.Error("mask dropped the secretRef reference")
	}
	if in[0].Spec.OnFailure.WebhookURL != hook {
		t.Error("masking mutated the caller's (cache's) cron")
	}
	if viewer[1].Spec.OnFailure != nil {
		t.Error("mask invented an onFailure block")
	}

	admin := maskCronsIfNeeded(maskAdminCtx(), nil, "p1", in)
	if got := admin[0].Spec.OnFailure.WebhookURL; got != hook {
		t.Errorf("admin sees webhookURL %q, want plaintext", got)
	}

	one := cronWithWebhook(hook)
	if got := maskCronIfNeeded(maskViewerCtx(), nil, "p1", &one).Spec.OnFailure.WebhookURL; got != envMaskSentinel {
		t.Errorf("single-cron mask: got %q", got)
	}
}

// A client that read the mask and PATCHes it back (e.g. to change only the
// signing secretRef) must keep the stored URL, not store the sentinel.
func TestResolveCronWebhookSentinel(t *testing.T) {
	t.Parallel()
	const hook = "https://hooks.example.com/abc"
	existing := cronWithWebhook(hook).Spec.OnFailure

	req := &crons.OnFailureUpdate{WebhookURL: envMaskSentinel, SecretRef: &kube.KusoSecretKeyRef{Name: "p1-new", Key: "k"}}
	if err := resolveCronWebhookSentinel(req, existing); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.WebhookURL != hook {
		t.Errorf("sentinel resolved to %q, want stored %q", req.WebhookURL, hook)
	}
	if req.SecretRef.Name != "p1-new" {
		t.Error("resolve clobbered the incoming secretRef")
	}

	fresh := &crons.OnFailureUpdate{WebhookURL: "https://new.example.com"}
	if err := resolveCronWebhookSentinel(fresh, existing); err != nil || fresh.WebhookURL != "https://new.example.com" {
		t.Errorf("real URL changed: %q err=%v", fresh.WebhookURL, err)
	}

	if err := resolveCronWebhookSentinel(&crons.OnFailureUpdate{WebhookURL: envMaskSentinel}, nil); err == nil {
		t.Error("sentinel with nothing stored must be rejected, not stored as the URL")
	}
}
