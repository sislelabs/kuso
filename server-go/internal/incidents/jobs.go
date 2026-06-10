package incidents

import (
	"context"
	"fmt"
	"log/slog"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// DefaultAgentImage is the in-cluster incident-agent image. Carries Claude
// Code CLI + kuso CLI + kubectl + git + gh. Overridable per-Spawner.
const DefaultAgentImage = "ghcr.io/sislelabs/kuso-incident-agent:latest"

// agentServiceAccount is the read-only SA the investigate Job runs under
// (deploy/incident-agent-rbac.yaml: pods/nodes/events get+list+watch + logs).
const agentServiceAccount = "kuso-incident-agent"

// ccSecretName holds the operator's Claude Code OAuth creds; mounted into
// the agent pod so `claude -p` authenticates against the operator's sub.
const ccSecretName = "kuso-incident-agent-cc"

// ccMountPath is where the CC credentials secret lands. The image's
// entrypoint symlinks /cc/credentials.json into ~/.claude/.
const ccMountPath = "/cc"

// jobNamespace is the control-plane namespace the agent Jobs run in when a
// Spawner doesn't override it.
const jobNamespace = "kuso"

// RepoInfo is the resolved repo coordinates + push token for the implement
// phase. The RepoResolver produces it from a db.Incident (project/service →
// KusoService CR repo + a minted GitHub App installation token). Zero value
// is valid (the agent reports "no repo wired").
type RepoInfo struct {
	Owner         string
	Name          string
	DefaultBranch string
	GitToken      string // short-lived GitHub App installation token
}

// RepoResolver resolves the repo + push token for an incident's implement
// phase. Implemented by the Manager's github-backed resolver; injected so
// the spawner stays decoupled from kube CR reads + token minting (and is
// testable with a stub). A nil resolver → empty RepoInfo.
type RepoResolver interface {
	Resolve(ctx context.Context, in db.Incident) (RepoInfo, error)
}

// KubeSpawner is the real Spawner: it launches the in-cluster agent Job for
// each incident phase. Mirrors the pkgupdates.buildApplyJob conventions
// (deterministic name for dedup, BackoffLimit + TTL cleanup, RestartNever).
type KubeSpawner struct {
	Kube       *kube.Client
	Namespace  string
	APIBaseURL string // base URL the agent hits, e.g. http://kuso-server.kuso.svc:8080
	AgentImage string
	Repos      RepoResolver // optional; resolves repo + git token for implement
	CloneOnly  bool         // test mode: implement Job clones + branches, skips the PR
	Logger     *slog.Logger
}

// investigateJobName / implementJobName are deterministic per incident so a
// re-spawn collides with the in-flight Job (IsAlreadyExists) rather than
// stacking duplicate agents. Incident ids are RFC-1123-safe hex.
func investigateJobName(id string) string { return "kuso-incident-" + id + "-investigate" }
func implementJobName(id string) string   { return "kuso-incident-" + id + "-implement" }

func (s *KubeSpawner) namespace() string {
	if s.Namespace != "" {
		return s.Namespace
	}
	return jobNamespace
}

func (s *KubeSpawner) image() string {
	if s.AgentImage != "" {
		return s.AgentImage
	}
	return DefaultAgentImage
}

func (s *KubeSpawner) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// SpawnInvestigate launches the read-only investigation Job. The agent
// root-causes via the kuso CLI / kubectl and POSTs its writeup to
// /api/incidents/{id}/findings.
func (s *KubeSpawner) SpawnInvestigate(ctx context.Context, in db.Incident) (string, error) {
	return s.create(ctx, s.buildJob(in, "investigate", RepoInfo{}))
}

// SpawnImplement launches the implement Job (same image/creds). The agent
// clones the repo, writes + validates the fix, pushes a branch, and POSTs
// /api/incidents/{id}/pr; the server opens the PR.
func (s *KubeSpawner) SpawnImplement(ctx context.Context, in db.Incident) (string, error) {
	var repo RepoInfo
	if s.Repos != nil {
		r, err := s.Repos.Resolve(ctx, in)
		if err != nil {
			// Non-fatal: spawn anyway with empty repo info so the agent can
			// report the wiring gap in the incident thread (better signal
			// than a silent no-spawn).
			s.log().Warn("incident: resolve repo for implement", "id", in.ID, "err", err)
		} else {
			repo = r
		}
	}
	return s.create(ctx, s.buildJob(in, "implement", repo))
}

// create submits the Job, treating AlreadyExists as success (the
// deterministic name means a concurrent re-spawn is the same logical run).
func (s *KubeSpawner) create(ctx context.Context, job *batchv1.Job) (string, error) {
	if s.Kube == nil {
		return "", fmt.Errorf("incidents: no kube client")
	}
	name := job.Name
	if _, err := s.Kube.Clientset.BatchV1().Jobs(s.namespace()).Create(ctx, job, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create %s job: %w", name, err)
	}
	s.log().Info("incident: agent job created", "job", name, "ns", s.namespace())
	return name, nil
}

// buildJob renders the agent Job for a phase ("investigate" | "implement").
// Pure (no kube calls) so it's unit-testable against a nil-Kube Spawner.
func (s *KubeSpawner) buildJob(in db.Incident, phase string, repo RepoInfo) *batchv1.Job {
	name := investigateJobName(in.ID)
	if phase == "implement" {
		name = implementJobName(in.ID)
	}

	// BackoffLimit 0: a failed investigation must NOT re-run — a crash-looping
	// service would otherwise drive an agent re-run storm (and each run burns
	// the operator's CC subscription). The single run IS the attempt; the
	// operator re-triggers via feedback if it failed.
	var backoff int32 = 0
	// Keep finished Jobs for a day so the operator can read agent stdout
	// before kube GCs the pod.
	var ttl int32 = 86400

	// Feedback so far, newline-joined, for the agent prompt (re-investigate
	// after operator correction, or the "go" that triggered implement).
	feedback := ""
	for _, f := range in.Feedback {
		if f.Text != "" {
			feedback += f.Text + "\n"
		}
	}

	env := []corev1.EnvVar{
		{Name: "INCIDENT_ID", Value: in.ID},
		{Name: "PHASE", Value: phase},
		{Name: "KUSO_API_URL", Value: s.APIBaseURL},
		// INCIDENT_TOKEN scopes the agent's incident-facing callbacks
		// (/findings, /pr) to this single incident. It is NOT a kuso API
		// token — it only authenticates those two endpoints.
		{Name: "INCIDENT_TOKEN", Value: in.AgentToken},
		// KUSO_TOKEN is intentionally EMPTY in v1. It must NOT reuse the
		// incident bearer (that would let a leaked kuso-CLI token also write
		// the incident record). Until a project-scoped viewer+sql token is
		// minted at spawn time, the agent investigates via kubectl using its
		// read-only SA token (the entrypoint falls back to kubectl when
		// KUSO_TOKEN is empty). TODO(integrator): mint + inject a scoped token.
		{Name: "KUSO_TOKEN", Value: ""},
		{Name: "CONTEXT_PACK", Value: string(in.ContextPack)},
		{Name: "PROJECT", Value: in.Project},
		{Name: "SERVICE", Value: in.Service},
		// Incident metadata the entrypoint renders into the agent prompt.
		{Name: "EVENT_TYPE", Value: in.EventType},
		{Name: "INCIDENT_TITLE", Value: in.Title},
		{Name: "SEVERITY", Value: in.Severity},
		{Name: "FEEDBACK", Value: feedback},
	}

	if phase == "implement" {
		// The implement phase needs the repo coordinates + a push token,
		// resolved at spawn time (repo from the KusoService CR, token minted
		// from the GitHub App). Empty values → the agent surfaces "no repo
		// wired" rather than the spawn failing.
		env = append(env,
			corev1.EnvVar{Name: "FINDINGS", Value: in.Findings},
			corev1.EnvVar{Name: "REPO_OWNER", Value: repo.Owner},
			corev1.EnvVar{Name: "REPO_NAME", Value: repo.Name},
			corev1.EnvVar{Name: "REPO_DEFAULT_BRANCH", Value: orDefault(repo.DefaultBranch, "main")},
			corev1.EnvVar{Name: "GIT_TOKEN", Value: repo.GitToken},
		)
		if s.CloneOnly {
			// Plumbing test: clone + branch, then stop before opening a PR.
			env = append(env, corev1.EnvVar{Name: "KUSO_INCIDENT_CLONE_ONLY", Value: "true"})
		}
	}

	labels := map[string]string{
		"app.kubernetes.io/name":      "kuso-incident-agent",
		"kuso.sislelabs.com/incident": in.ID,
		"kuso.sislelabs.com/phase":    phase,
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.namespace(),
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: agentServiceAccount,
					Volumes: []corev1.Volume{{
						Name: "cc-creds",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: ccSecretName,
							},
						},
					}},
					Containers: []corev1.Container{{
						Name:  "agent",
						Image: s.image(),
						Env:   env,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "cc-creds",
							MountPath: ccMountPath,
							ReadOnly:  true,
						}},
					}},
				},
			},
		},
	}
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
