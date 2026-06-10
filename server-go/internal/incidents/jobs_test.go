package incidents

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/db"
)

func TestJobName(t *testing.T) {
	if got := investigateJobName("inc-abcd1234"); got != "kuso-incident-inc-abcd1234-investigate" {
		t.Errorf("investigateJobName = %q", got)
	}
	if got := implementJobName("inc-abcd1234"); got != "kuso-incident-inc-abcd1234-implement" {
		t.Errorf("implementJobName = %q", got)
	}
}

// envMap flattens the container env into a name→value map for assertions.
func envMap(env []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		m[e.Name] = e.Value
	}
	return m
}

func TestBuildJob(t *testing.T) {
	in := db.Incident{
		ID:          "inc-deadbeef",
		Project:     "shop",
		Service:     "checkout",
		AgentToken:  "iat-cafef00dbabe",
		ContextPack: json.RawMessage(`{"type":"pod.crashed","title":"checkout CrashLoopBackOff"}`),
	}
	s := &KubeSpawner{
		// nil Kube on purpose: buildJob must not touch the cluster.
		APIBaseURL: "http://kuso-server.kuso.svc:8080",
		AgentImage: "ghcr.io/sislelabs/kuso-incident-agent:v1",
	}

	tests := []struct {
		phase      string
		wantName   string
		wantGitEnv bool
	}{
		{"investigate", "kuso-incident-inc-deadbeef-investigate", false},
		{"implement", "kuso-incident-inc-deadbeef-implement", true},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			repo := RepoInfo{}
			if tt.phase == "implement" {
				repo = RepoInfo{Owner: "o", Name: "r", DefaultBranch: "main", GitToken: "ght"}
			}
			job := s.buildJob(in, tt.phase, repo)

			if job.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", job.Name, tt.wantName)
			}
			if job.Namespace != jobNamespace {
				t.Errorf("Namespace = %q, want %q", job.Namespace, jobNamespace)
			}
			if job.Labels["kuso.sislelabs.com/incident"] != in.ID {
				t.Errorf("incident label = %q", job.Labels["kuso.sislelabs.com/incident"])
			}
			if job.Labels["kuso.sislelabs.com/phase"] != tt.phase {
				t.Errorf("phase label = %q", job.Labels["kuso.sislelabs.com/phase"])
			}

			// Backoff 0 (no re-run storm) + TTL 1d.
			if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
				t.Errorf("BackoffLimit = %v, want 0", job.Spec.BackoffLimit)
			}
			if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 86400 {
				t.Errorf("TTLSecondsAfterFinished = %v, want 86400", job.Spec.TTLSecondsAfterFinished)
			}

			pod := job.Spec.Template.Spec
			if pod.RestartPolicy != corev1.RestartPolicyNever {
				t.Errorf("RestartPolicy = %q, want Never", pod.RestartPolicy)
			}
			if pod.ServiceAccountName != agentServiceAccount {
				t.Errorf("ServiceAccountName = %q, want %q", pod.ServiceAccountName, agentServiceAccount)
			}

			if len(pod.Containers) != 1 {
				t.Fatalf("Containers = %d, want 1", len(pod.Containers))
			}
			c := pod.Containers[0]
			if c.Image != "ghcr.io/sislelabs/kuso-incident-agent:v1" {
				t.Errorf("Image = %q", c.Image)
			}

			// CC creds secret mounted read-only at /cc.
			if len(pod.Volumes) != 1 || pod.Volumes[0].Secret == nil ||
				pod.Volumes[0].Secret.SecretName != ccSecretName {
				t.Fatalf("CC secret volume not wired: %+v", pod.Volumes)
			}
			if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != ccMountPath ||
				!c.VolumeMounts[0].ReadOnly {
				t.Errorf("CC volume mount not wired: %+v", c.VolumeMounts)
			}

			// Env contract.
			env := envMap(c.Env)
			if env["INCIDENT_ID"] != in.ID {
				t.Errorf("INCIDENT_ID = %q", env["INCIDENT_ID"])
			}
			if env["PHASE"] != tt.phase {
				t.Errorf("PHASE = %q, want %q", env["PHASE"], tt.phase)
			}
			if env["KUSO_API_URL"] != s.APIBaseURL {
				t.Errorf("KUSO_API_URL = %q", env["KUSO_API_URL"])
			}
			if env["INCIDENT_TOKEN"] != in.AgentToken {
				t.Errorf("INCIDENT_TOKEN = %q", env["INCIDENT_TOKEN"])
			}
			// KUSO_TOKEN must NOT reuse the incident bearer (least privilege):
			// empty in v1 (agent falls back to the read-only SA + kubectl).
			if env["KUSO_TOKEN"] != "" {
				t.Errorf("KUSO_TOKEN = %q, want empty (must not reuse AgentToken)", env["KUSO_TOKEN"])
			}
			if env["CONTEXT_PACK"] != string(in.ContextPack) {
				t.Errorf("CONTEXT_PACK = %q", env["CONTEXT_PACK"])
			}
			if env["PROJECT"] != in.Project {
				t.Errorf("PROJECT = %q", env["PROJECT"])
			}
			if env["SERVICE"] != in.Service {
				t.Errorf("SERVICE = %q", env["SERVICE"])
			}

			// GIT_TOKEN only present on implement.
			_, hasGit := env["GIT_TOKEN"]
			if hasGit != tt.wantGitEnv {
				t.Errorf("GIT_TOKEN present = %v, want %v", hasGit, tt.wantGitEnv)
			}
		})
	}
}

func TestBuildJobDefaultImage(t *testing.T) {
	s := &KubeSpawner{} // no AgentImage → default
	job := s.buildJob(db.Incident{ID: "inc-x"}, "investigate", RepoInfo{})
	if got := job.Spec.Template.Spec.Containers[0].Image; got != DefaultAgentImage {
		t.Errorf("Image = %q, want default %q", got, DefaultAgentImage)
	}
}
