package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

// Sending channelId:"" cleared nothing: the handler only wrote non-empty
// values, so the bot kept posting to the old channel.
func TestPutDiscord_ChannelIDClearAndKeep(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: botConfigName, Namespace: "kuso"},
		Data:       map[string]string{channelConfigKey: "123"},
	})
	h := &IncidentAgentSettingsHandler{
		Kube:      &kube.Client{Clientset: cs},
		Namespace: "kuso",
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	put := func(body string) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, "/api/admin/settings/incident-agent/discord", strings.NewReader(body))
		ctx := auth.WithClaimsForTest(r.Context(), &auth.Claims{Permissions: []string{string(auth.PermSettingsAdmin)}})
		rr := httptest.NewRecorder()
		h.PutDiscord(rr, r.WithContext(ctx))
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		cm, err := cs.CoreV1().ConfigMaps("kuso").Get(context.Background(), botConfigName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get configmap: %v", err)
		}
		return cm.Data[channelConfigKey]
	}
	if got := put(`{"botToken":"t"}`); got != "123" {
		t.Fatalf("omitted channelId must keep the channel, got %q", got)
	}
	if got := put(`{"channelId":""}`); got != "" {
		t.Fatalf(`channelId:"" must clear the channel, got %q`, got)
	}
}
