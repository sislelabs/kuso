package incidents

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// DefaultAgentImage is the in-cluster incident-agent image. Carries Claude
// Code CLI + kuso CLI + kubectl + git + gh. Overridable per-Spawner.
const DefaultAgentImage = "ghcr.io/sislelabs/kuso-incident-agent:latest@sha256:a5f3a17828063b612f4cd30c56ff128a6ca0c7964b474e1e77c7e539f1c4e3aa"

// agentServiceAccount is the read-only SA the investigate Job runs under
// (deploy/incident-agent-rbac.yaml: pods/nodes/events get+list+watch).
// Log reads are granted per incident namespace (ensureLogAccess).
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

// Per-Job Secret keys. The incident bearer and the git token reach the
// container through the Secret, never as literals in the Job spec (which
// anyone with jobs get could read for the Job's 24h TTL). The key names
// are the file names the entrypoint reads from agentSecretsDir.
const (
	secretKeyIncidentToken = "incident-token"
	secretKeyGitToken      = "git-token"

	jobSecretMount  = "/run/agent-secret"
	agentSecretsDir = "/run/kuso-agent"
)

func ptrQuantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

// logAccessName names both the ClusterRole (pods/log get+list, shipped in
// deploy/incident-agent-rbac.yaml) and the RoleBinding stamped into the
// incident's namespace so the agent can read logs THERE and nowhere else.
const logAccessName = "kuso-incident-agent-logs"

// Labels on agent Jobs/pods. labelLogNamespace records which namespace a
// running agent was granted log access to, so the reaper can revoke
// bindings no live agent needs.
const (
	labelApp          = "app.kubernetes.io/name"
	labelAppAgent     = "kuso-incident-agent"
	labelIncident     = "kuso.sislelabs.com/incident"
	labelPhase        = "kuso.sislelabs.com/phase"
	labelLogNamespace = "kuso.sislelabs.com/log-namespace"
)

// kusoServerPort is the kuso-server container port (Service :80 → 3000).
// NetworkPolicy matches post-DNAT, so it's the pod port that matters.
const kusoServerPort = 3000

// RepoInfo is the resolved repo coordinates + push token for the implement
// phase. The RepoResolver produces it from a db.Incident (project/service →
// KusoService CR repo + a minted repo-scoped GitHub App token). Zero value
// is valid (the agent reports "no repo wired").
type RepoInfo struct {
	Owner         string
	Name          string
	DefaultBranch string
	GitToken      string // short-lived token scoped to Owner/Name only
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
	Repos      RepoResolver   // optional; resolves repo + git token for implement
	Config     ConfigProvider // optional; an AgentImage override here wins over the field
	CloneOnly  bool           // test mode: implement Job clones + branches, skips the PR
	// ProjectNamespace maps a project to the namespace its workloads run
	// in (kube.ProjectNamespaceResolver.NamespaceFor). nil → the Job
	// namespace, which is where legacy (non-custom-ns) projects live.
	ProjectNamespace func(ctx context.Context, project string) string
	Logger           *slog.Logger
}

// investigateJobName / implementJobName are deterministic per incident so a
// re-spawn collides with the in-flight Job (IsAlreadyExists) rather than
// stacking duplicate agents. Incident ids are RFC-1123-safe hex.
func investigateJobName(id string) string { return "kuso-incident-" + id + "-investigate" }
func implementJobName(id string) string   { return "kuso-incident-" + id + "-implement" }

func agentSecretName(jobName string) string  { return jobName + "-creds" }
func egressPolicyName(jobName string) string { return jobName + "-egress" }

func (s *KubeSpawner) namespace() string {
	if s.Namespace != "" {
		return s.Namespace
	}
	return jobNamespace
}

func (s *KubeSpawner) image() string {
	// Config override (settings UI) wins, so the image hot-reloads.
	if s.Config != nil {
		if img := s.Config.Get(context.Background()).AgentImage; img != "" {
			return img
		}
	}
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

// logNamespace is the one namespace the agent may read pod logs in.
// Project-less incidents (node.unreachable) get none, and so do projects
// in the home namespace: a binding there would cover every home-namespace
// tenant plus kuso-server itself, which a prompt-injected log line could
// steer the agent into reading.
func (s *KubeSpawner) logNamespace(ctx context.Context, in db.Incident) string {
	if in.Project == "" || s.ProjectNamespace == nil {
		return ""
	}
	if ns := s.ProjectNamespace(ctx, in.Project); ns != "" && ns != s.namespace() {
		return ns
	}
	return ""
}

// SpawnInvestigate launches the read-only investigation Job. The agent
// root-causes via kubectl (scoped to the incident namespace) and the
// entrypoint POSTs its writeup to /api/incidents/{id}/findings.
func (s *KubeSpawner) SpawnInvestigate(ctx context.Context, in db.Incident) (string, error) {
	return s.launch(ctx, in, "investigate", RepoInfo{})
}

// SpawnImplement launches the implement Job (same image/creds). The agent
// clones the repo and writes + validates the fix; the entrypoint pushes
// the branch, opens the PR and reports it to /api/incidents/{id}/pr.
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
	return s.launch(ctx, in, "implement", repo)
}

// launch creates the Job suspended, attaches its owned Secret and egress
// NetworkPolicy (so both are GC'd with the Job and the pod never runs
// before its egress policy exists), grants log access in the incident
// namespace, then resumes the Job.
func (s *KubeSpawner) launch(ctx context.Context, in db.Incident, phase string, repo RepoInfo) (string, error) {
	if s.Kube == nil {
		return "", fmt.Errorf("incidents: no kube client")
	}
	ns := s.namespace()
	logNS := s.logNamespace(ctx, in)
	job := s.buildJob(in, phase, repo, logNS)
	name := job.Name
	jobs := s.Kube.Clientset.BatchV1().Jobs(ns)

	created, err := jobs.Create(ctx, job, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Deterministic name: a concurrent re-spawn is the same logical run.
		return name, nil
	}
	if err != nil {
		return "", fmt.Errorf("create %s job: %w", name, err)
	}
	owner := metav1.OwnerReference{
		APIVersion: "batch/v1", Kind: "Job", Name: created.Name, UID: created.UID,
	}
	abort := func(cause error) (string, error) {
		bg := metav1.DeletePropagationBackground
		if derr := jobs.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg}); derr != nil {
			s.log().Warn("incident: delete half-created agent job", "job", name, "err", derr)
		}
		return "", cause
	}

	sec := buildAgentSecret(in, phase, repo, name, ns, owner)
	if _, err := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		// No plaintext fallback: without the Secret the tokens have nowhere
		// safe to live.
		return abort(fmt.Errorf("create %s secret: %w", sec.Name, err))
	}

	// Fail closed: the agent reads attacker-influenced logs, so it never
	// runs without its egress policy.
	np := buildEgressPolicy(in.ID, phase, name, ns, owner)
	if _, err := s.Kube.Clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, np, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return abort(fmt.Errorf("create %s egress policy (agent not started): %w", np.Name, err))
	}

	if phase == "investigate" && logNS != "" {
		if err := s.ensureLogAccess(ctx, logNS); err != nil {
			s.log().Warn("incident: could not grant agent log access in incident namespace",
				"ns", logNS, "err", err)
		}
	}

	patch := []byte(`{"spec":{"suspend":false}}`)
	if _, err := jobs.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return abort(fmt.Errorf("resume %s job: %w", name, err))
	}
	s.log().Info("incident: agent job created", "job", name, "ns", ns, "logNamespace", logNS)
	return name, nil
}

// ensureLogAccess binds the logs ClusterRole to the agent SA inside ns
// only. The binding is long-lived (kuso-server can't delete RoleBindings);
// the reaper empties its subjects once no live agent needs ns.
func (s *KubeSpawner) ensureLogAccess(ctx context.Context, ns string) error {
	rbs := s.Kube.Clientset.RbacV1().RoleBindings(ns)
	want := logRoleBinding(ns, s.namespace())
	cur, err := rbs.Get(ctx, logAccessName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = rbs.Create(ctx, want, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if len(cur.Subjects) == 1 && cur.Subjects[0] == want.Subjects[0] {
		return nil
	}
	cur.Subjects = want.Subjects
	_, err = rbs.Update(ctx, cur, metav1.UpdateOptions{})
	return err
}

func logRoleBinding(ns, saNamespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      logAccessName,
			Namespace: ns,
			Labels:    map[string]string{labelApp: logAccessName},
		},
		RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: logAccessName},
		Subjects: []rbacv1.Subject{{
			Kind: rbacv1.ServiceAccountKind, Name: agentServiceAccount, Namespace: saNamespace,
		}},
	}
}

// buildAgentSecret holds the per-Job credentials, owned by the Job.
func buildAgentSecret(in db.Incident, phase string, repo RepoInfo, jobName, ns string, owner metav1.OwnerReference) *corev1.Secret {
	data := map[string][]byte{secretKeyIncidentToken: []byte(in.AgentToken)}
	if phase == "implement" {
		data[secretKeyGitToken] = []byte(repo.GitToken)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            agentSecretName(jobName),
			Namespace:       ns,
			Labels:          map[string]string{labelApp: labelAppAgent, labelIncident: in.ID, labelPhase: phase},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

// privateRanges are excluded from the agent's open HTTPS egress so it
// can't reach in-cluster services (addon DBs, registry, other tenants)
// or the node network; only the explicit kuso-server + DNS + apiserver
// rules below reach inside.
var privateRanges = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8",
}

// buildEgressPolicy restricts the agent pod to: DNS, kuso-server, the
// kube-apiserver (investigate only: kubectl; post-DNAT it's a node IP on
// :6443), and HTTPS to public addresses (api.anthropic.com, github.com:
// no stable IPs, so public :443 is the realistic floor).
func buildEgressPolicy(incidentID, phase, jobName, ns string, owner metav1.OwnerReference) *networkingv1.NetworkPolicy {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	port := func(p int, proto corev1.Protocol) networkingv1.NetworkPolicyPort {
		v := intstr.FromInt32(int32(p))
		return networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &v}
	}
	egress := []networkingv1.NetworkPolicyEgressRule{
		{Ports: []networkingv1.NetworkPolicyPort{port(53, udp), port(53, tcp)}},
		{
			To: []networkingv1.NetworkPolicyPeer{{
				PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{labelApp: "kuso-server"}},
			}},
			Ports: []networkingv1.NetworkPolicyPort{port(kusoServerPort, tcp)},
		},
	}
	if phase == "investigate" {
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{Ports: []networkingv1.NetworkPolicyPort{port(6443, tcp)}})
	}
	egress = append(egress, networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: privateRanges},
		}},
		Ports: []networkingv1.NetworkPolicyPort{port(443, tcp)},
	})
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            egressPolicyName(jobName),
			Namespace:       ns,
			Labels:          map[string]string{labelApp: labelAppAgent, labelIncident: incidentID, labelPhase: phase},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
				labelIncident: incidentID, labelPhase: phase,
			}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: egress,
		},
	}
}

// buildJob renders the agent Job for a phase ("investigate" | "implement").
// Pure (no kube calls) so it's unit-testable against a nil-Kube Spawner.
// The Job is created suspended; launch resumes it once the Secret and
// egress policy it depends on exist.
func (s *KubeSpawner) buildJob(in db.Incident, phase string, repo RepoInfo, logNS string) *batchv1.Job {
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
	// A hung agent otherwise runs (and holds its MaxConcurrent slot)
	// forever; the reaper's investigate timeout is the same bound.
	deadline := int64(investigateTimeout.Seconds())
	suspend := true
	automountSA := phase == "investigate"
	nonRoot, noEscalation := true, false
	// uid/gid of the image's "node" user, numeric so the kubelet can
	// verify runAsNonRoot.
	agentUID := int64(1000)

	// Feedback so far, newline-joined, for the agent prompt (re-investigate
	// after operator correction, or the "go" that triggered implement).
	feedback := ""
	for _, f := range in.Feedback {
		if f.Text != "" {
			feedback += f.Text + "\n"
		}
	}

	// The incident bearer (scoped to /findings and /pr of this one
	// incident) and the git token reach the agent container as files, not
	// env: a container's env sits in /proc/1/environ for its whole life,
	// readable by every process the agent starts. The init container
	// copies them into a memory emptyDir; the entrypoint reads and deletes
	// them before the agent runs.
	env := []corev1.EnvVar{
		{Name: "INCIDENT_ID", Value: in.ID},
		{Name: "PHASE", Value: phase},
		{Name: "KUSO_API_URL", Value: s.APIBaseURL},
		{Name: "SECRETS_DIR", Value: agentSecretsDir},
		// KUSO_TOKEN is intentionally EMPTY in v1. It must NOT reuse the
		// incident bearer (that would let a leaked kuso-CLI token also write
		// the incident record). Until a project-scoped viewer+sql token is
		// minted at spawn time, the agent investigates via kubectl using its
		// read-only SA token (the entrypoint falls back to kubectl when
		// KUSO_TOKEN is empty). TODO(integrator): mint + inject a scoped token.
		{Name: "KUSO_TOKEN", Value: ""},
		{Name: "PROJECT", Value: in.Project},
		{Name: "SERVICE", Value: in.Service},
		{Name: "LOG_NAMESPACE", Value: logNS},
		// Incident metadata the entrypoint renders into the agent prompt.
		{Name: "EVENT_TYPE", Value: in.EventType},
		{Name: "INCIDENT_TITLE", Value: in.Title},
		{Name: "SEVERITY", Value: in.Severity},
		{Name: "FEEDBACK", Value: feedback},
	}

	if phase == "investigate" {
		// The context pack carries the crashing pod's log tail: attacker-
		// influenced text. Only the read-only phase sees it; the implement
		// phase works from the findings the operator approved.
		env = append(env, corev1.EnvVar{Name: "CONTEXT_PACK", Value: string(in.ContextPack)})
	}

	if phase == "implement" {
		// Repo coordinates + a push token for that one repo, resolved at
		// spawn time. Empty values → the agent surfaces "no repo wired"
		// rather than the spawn failing.
		env = append(env,
			corev1.EnvVar{Name: "FINDINGS", Value: in.Findings},
			corev1.EnvVar{Name: "REPO_OWNER", Value: repo.Owner},
			corev1.EnvVar{Name: "REPO_NAME", Value: repo.Name},
			corev1.EnvVar{Name: "REPO_DEFAULT_BRANCH", Value: orDefault(repo.DefaultBranch, "main")},
		)
		if RunRepoCode() {
			// Opt-in: lets the agent run the repo's build/test commands,
			// i.e. code from the repo with the same uid that holds the
			// Claude credentials and public HTTPS egress.
			env = append(env, corev1.EnvVar{Name: "AGENT_RUN_REPO_CODE", Value: "true"})
		}
		if s.CloneOnly {
			// Plumbing test: clone + branch, then stop before opening a PR.
			env = append(env, corev1.EnvVar{Name: "KUSO_INCIDENT_CLONE_ONLY", Value: "true"})
		}
	}

	labels := map[string]string{
		labelApp:      labelAppAgent,
		labelIncident: in.ID,
		labelPhase:    phase,
	}
	jobLabels := map[string]string{}
	for k, v := range labels {
		jobLabels[k] = v
	}
	if logNS != "" && phase == "investigate" {
		jobLabels[labelLogNamespace] = logNS
	}

	containerSC := &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noEscalation,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	secretMode := int32(0o444)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.namespace(),
			Labels:    jobLabels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &deadline,
			Suspend:                 &suspend,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: agentServiceAccount,
					// Only investigate drives kubectl; the implement
					// phase gets no kube credentials at all.
					AutomountServiceAccountToken: &automountSA,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &nonRoot,
						RunAsUser:      &agentUID,
						RunAsGroup:     &agentUID,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: []corev1.Volume{
						{Name: "cc-creds", VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: ccSecretName},
						}},
						{Name: "job-secret", VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: agentSecretName(name), DefaultMode: &secretMode},
						}},
						{Name: "agent-secrets", VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptrQuantity("1Mi")},
						}},
					},
					// Only this container mounts the per-Job Secret.
					InitContainers: []corev1.Container{{
						Name:            "secrets",
						Image:           s.image(),
						Command:         []string{"/bin/sh", "-c", "umask 077 && cp " + jobSecretMount + "/* " + agentSecretsDir + "/"},
						SecurityContext: containerSC,
						VolumeMounts: []corev1.VolumeMount{
							{Name: "job-secret", MountPath: jobSecretMount, ReadOnly: true},
							{Name: "agent-secrets", MountPath: agentSecretsDir},
						},
					}},
					Containers: []corev1.Container{{
						Name:            "agent",
						Image:           s.image(),
						Env:             env,
						SecurityContext: containerSC,
						VolumeMounts: []corev1.VolumeMount{
							{Name: "cc-creds", MountPath: ccMountPath, ReadOnly: true},
							{Name: "agent-secrets", MountPath: agentSecretsDir},
						},
					}},
				},
			},
		},
	}
}

// RunRepoCode reports whether the implement phase may run the repo's
// build and test commands (KUSO_INCIDENT_AGENT_RUN_REPO_CODE=true on
// kuso-server). Off by default.
func RunRepoCode() bool { return os.Getenv("KUSO_INCIDENT_AGENT_RUN_REPO_CODE") == "true" }

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
