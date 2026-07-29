package projects

import (
	"testing"

	"kuso/server/internal/kube"
)

// TestEnvSleepFrom_ExcludePathsNotActivatorRouted is the HIGH-5c guard:
// a service with wakeOn.excludePaths is kept warm (never scaled to zero),
// so it must NOT be routed through the activator — otherwise the activator
// becomes a hard dependency for 100% of the webhook/payment traffic that
// excludePaths exists to protect.
func TestEnvSleepFrom_ExcludePathsNotActivatorRouted(t *testing.T) {
	t.Parallel()

	// Plain sleep, no excludePaths → activator-routed (Enabled).
	plain := envSleepFrom(&kube.KusoServiceSleep{Enabled: true})
	if plain == nil || !plain.Enabled {
		t.Fatalf("plain sleep should be activator-routed, got %+v", plain)
	}

	// Sleep + excludePaths → NOT activator-routed (nil).
	excl := envSleepFrom(&kube.KusoServiceSleep{
		Enabled: true,
		WakeOn:  &kube.KusoServiceWake{ExcludePaths: []string{"/stripe/webhook"}},
	})
	if excl != nil {
		t.Fatalf("excludePaths service must not be activator-routed, got %+v", excl)
	}

	// Sleep disabled → nil regardless.
	if envSleepFrom(&kube.KusoServiceSleep{Enabled: false}) != nil {
		t.Fatal("disabled sleep should map to nil")
	}
	if envSleepFrom(nil) != nil {
		t.Fatal("nil sleep should map to nil")
	}
}
