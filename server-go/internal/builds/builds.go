// Package builds owns the KusoBuild CR lifecycle: list/get/create plus a
// status poller that watches the rendered Job (via batch/v1) and
// promotes the image tag onto the production env on success.
//
// Phase 5 ships build creation that accepts an explicit ref or a public-
// repo branch (synthetic ref). Branch → SHA resolution via the GitHub
// App lands in Phase 6 once the github package is wired.
package builds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// RegistryHost is the in-cluster registry every build pushes to. The
// helm chart for kuso-registry exposes this as a Service.
const RegistryHost = "kuso-registry.kuso.svc.cluster.local:5000"

// Service handles the build domain. Construct via New.
type Service struct {
	Kube      *kube.Client
	Namespace string
}

// New constructs a builds.Service with a default namespace fallback.
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

// Errors mirroring the rest of the codebase.
var (
	ErrNotFound = errors.New("builds: not found")
	ErrInvalid  = errors.New("builds: invalid")
)

// CreateBuildRequest is the body of POST /api/projects/:p/services/:s/builds.
type CreateBuildRequest struct {
	Branch string `json:"branch,omitempty"`
	Ref    string `json:"ref,omitempty"`
}

// shaRE matches a full 40-char git SHA.
var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// List returns the builds for a project (and optionally for a single
// service inside it), newest first.
func (s *Service) List(ctx context.Context, project, service string) ([]kube.KusoBuild, error) {
	selector := "kuso.sislelabs.com/project=" + project
	if service != "" {
		selector += ",kuso.sislelabs.com/service=" + project + "-" + service
	}
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(s.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	out := make([]kube.KusoBuild, 0, len(raw.Items))
	for i := range raw.Items {
		var b kube.KusoBuild
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &b); err != nil {
			return nil, fmt.Errorf("decode build: %w", err)
		}
		out = append(out, b)
	}
	// Newest first by creationTimestamp; ties break on name lexically so
	// the order is stable across calls.
	sort.SliceStable(out, func(i, j int) bool {
		ti := out[i].CreationTimestamp
		tj := out[j].CreationTimestamp
		if ti.Equal(&tj) {
			return out[i].Name > out[j].Name
		}
		return ti.After(tj.Time)
	})
	return out, nil
}

// Create persists a new KusoBuild. Phase 5 limits:
//   - explicit `ref` (40-char SHA) → image tag = first 12 chars
//   - explicit `ref` (anything else) → image tag = ref verbatim
//   - empty ref + branch + no GitHub installation → synthetic
//     "<branch>-<unix-millis>" tag (kaniko clones HEAD of the branch)
//   - branch → SHA via GitHub App is Phase 6
//
// The KusoBuild CR carries the resolved image repository + tag. The
// operator's helm-charts/kusobuild chart renders the kaniko Job; the
// poller picks up its outcome.
func (s *Service) Create(ctx context.Context, project, service string, req CreateBuildRequest) (*kube.KusoBuild, error) {
	fqn := project + "-" + service
	svcCR, err := s.Kube.GetKusoService(ctx, s.Namespace, fqn)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: service %s/%s", ErrNotFound, project, service)
	}
	if err != nil {
		return nil, fmt.Errorf("preflight service: %w", err)
	}
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: project %s", ErrNotFound, project)
	}
	if err != nil {
		return nil, fmt.Errorf("preflight project: %w", err)
	}

	repoURL := ""
	repoPath := "."
	if svcCR.Spec.Repo != nil {
		repoURL = svcCR.Spec.Repo.URL
		if svcCR.Spec.Repo.Path != "" {
			repoPath = svcCR.Spec.Repo.Path
		}
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}
	if repoURL == "" {
		return nil, fmt.Errorf("%w: service has no repo URL configured", ErrInvalid)
	}

	branch := req.Branch
	if branch == "" {
		if proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
			branch = proj.Spec.DefaultRepo.DefaultBranch
		} else {
			branch = "main"
		}
	}

	sha := req.Ref
	if !shaRE.MatchString(sha) {
		// Phase 5 cannot resolve branch → SHA via GitHub yet. Synthesize
		// a unique-ish ref. Phase 6 will replace this branch with the
		// real github resolve.
		sha = fmt.Sprintf("%s-%s", branch, strconv.FormatInt(time.Now().UnixMilli(), 36))
	}

	buildName := buildCRName(project, service, sha)
	imageRepo := fmt.Sprintf("%s/%s/%s", RegistryHost, project, service)

	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: buildName,
			Labels: map[string]string{
				"kuso.sislelabs.com/project":   project,
				"kuso.sislelabs.com/service":   fqn,
				"kuso.sislelabs.com/build-ref": shortRef(sha),
			},
		},
		Spec: kube.KusoBuildSpec{
			Project:              project,
			Service:              fqn,
			Ref:                  sha,
			Branch:               branch,
			Repo:                 &kube.KusoRepoRef{URL: repoURL, Path: repoPath},
			GithubInstallationID: githubInstallationID(proj),
			Strategy:             "dockerfile",
			Image:                &kube.KusoImage{Repository: imageRepo, Tag: ImageTag(sha)},
		},
	}
	return s.Kube.CreateKusoBuild(ctx, s.Namespace, build)
}

// ImageTag returns the canonical image tag for a ref: 12-char SHA prefix
// for full SHAs, otherwise the ref verbatim. Exported for the GitHub
// webhook handler in Phase 6.
func ImageTag(ref string) string {
	if shaRE.MatchString(ref) {
		return ref[:12]
	}
	return ref
}

func buildCRName(project, service, ref string) string {
	return fmt.Sprintf("%s-%s-%s", project, service, shortRef(ref))
}

func shortRef(ref string) string {
	if shaRE.MatchString(ref) {
		return ref[:12]
	}
	// Trim to a k8s-name-safe slug. Replace anything not [a-z0-9-] with
	// dashes and clip to 32 chars so the full build name stays under 63.
	const max = 32
	out := make([]byte, 0, len(ref))
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == '-':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return strings.Trim(string(out), "-")
}

func githubInstallationID(proj *kube.KusoProject) int64 {
	if proj == nil || proj.Spec.GitHub == nil {
		return 0
	}
	return proj.Spec.GitHub.InstallationID
}

// ---- Status poller -------------------------------------------------------

// Poller watches kaniko Jobs rendered for KusoBuilds and stamps their
// outcome onto KusoBuild.status. On success it patches the production
// KusoEnvironment with the new image tag.
type Poller struct {
	Svc      *Service
	Interval time.Duration
	Logger   *slog.Logger
}

// Run blocks until ctx is cancelled, ticking every Interval and updating
// any KusoBuild whose phase is not yet succeeded/failed. Returns ctx.Err
// on shutdown. Errors from individual ticks are logged at warn so we
// never silently lose state changes — the previous "_ = err" silenced a
// real bug for an entire test cycle.
func (p *Poller) Run(ctx context.Context) error {
	if p.Interval <= 0 {
		p.Interval = 30 * time.Second
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	tick := time.NewTicker(p.Interval)
	defer tick.Stop()
	for {
		if err := p.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.Logger.Warn("build poller tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (p *Poller) tick(ctx context.Context) error {
	raw, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(p.Svc.Namespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list builds: %w", err)
	}
	for i := range raw.Items {
		var b kube.KusoBuild
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &b); err != nil {
			continue
		}
		phase, _ := b.Status["phase"].(string)
		if phase == "succeeded" || phase == "failed" {
			continue
		}
		if err := p.checkBuild(ctx, &b); err != nil && !apierrors.IsNotFound(err) {
			p.Logger.Warn("build poller checkBuild", "build", b.Name, "err", err)
		}
	}
	return nil
}

// checkBuild reads the kaniko Job for one build and reconciles status.
func (p *Poller) checkBuild(ctx context.Context, b *kube.KusoBuild) error {
	job, err := p.Svc.Kube.Clientset.BatchV1().Jobs(p.Svc.Namespace).Get(ctx, b.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cond := completedCondition(job); cond != nil {
		if cond.Type == batchv1.JobComplete {
			return p.markSucceeded(ctx, b)
		}
		return p.markFailed(ctx, b, cond.Message)
	}
	if job.Status.Active > 0 {
		return p.markRunning(ctx, b)
	}
	return nil
}

func completedCondition(job *batchv1.Job) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		c := &job.Status.Conditions[i]
		if c.Status != "True" {
			continue
		}
		if c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed {
			return c
		}
	}
	return nil
}

// The KusoBuild CRD does NOT declare a /status subresource (see
// operator/config/crd/bases/application.kuso.sislelabs.com_kusobuilds.yaml
// — only spec/metadata properties, no `subresources: status: {}`).
// Calling Patch with the "status" subresource path therefore returns
// 404 "the server could not find the requested resource". The status
// stanza lives on the main resource; merge-patch it directly.
func (p *Poller) markSucceeded(ctx context.Context, b *kube.KusoBuild) error {
	patch := fmt.Sprintf(`{"status":{"phase":"succeeded","completedAt":%q}}`, time.Now().UTC().Format(time.RFC3339))
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(p.Svc.Namespace).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch build status: %w", err)
	}
	return p.promoteImage(ctx, b)
}

func (p *Poller) markFailed(ctx context.Context, b *kube.KusoBuild, msg string) error {
	patch := fmt.Sprintf(`{"status":{"phase":"failed","completedAt":%q,"message":%q}}`, time.Now().UTC().Format(time.RFC3339), msg)
	_, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(p.Svc.Namespace).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch build failed: %w", err)
	}
	return nil
}

func (p *Poller) markRunning(ctx context.Context, b *kube.KusoBuild) error {
	if phase, _ := b.Status["phase"].(string); phase == "running" {
		return nil
	}
	patch := []byte(`{"status":{"phase":"running"}}`)
	_, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(p.Svc.Namespace).
		Patch(ctx, b.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch build running: %w", err)
	}
	return nil
}

// logger returns the configured Logger or slog.Default(). Used so the
// poller's helper paths work in unit tests that construct a Poller
// directly without going through Run().
func (p *Poller) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// promoteImage patches the new image tag onto every KusoEnvironment
// whose branch matches the build's branch. That's:
//
//   - the production env when builds are triggered against the project
//     default branch (whether by a push webhook or a manual `build trigger`);
//   - the preview-pr-N env when builds are triggered against a PR head
//     ref (via the dispatcher's onPullRequest path);
//   - both envs when somebody manually triggers a build with the same
//     branch as the PR they're testing — that's a no-op for production
//     in practice because webhook flows separate the two.
//
// The TS server only patched production; preview envs sat at
// InvalidImageName until a human manually edited spec.image. Per the
// rewrite plan §5 / TS comment in github-webhooks.service.ts, that was
// a known-incomplete feature. We close it here by matching on
// spec.branch over the env list filtered to this build's service.
func (p *Poller) promoteImage(ctx context.Context, b *kube.KusoBuild) error {
	if b.Spec.Image == nil {
		return nil
	}
	// List every env in the namespace and filter on spec.service —
	// label-based filtering would be cheaper but the spec.service field
	// is the contract, while labels are best-effort metadata that may
	// be absent on hand-rolled CRs.
	raw, err := p.Svc.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(p.Svc.Namespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list envs for promotion: %w", err)
	}
	patch := fmt.Sprintf(`{"spec":{"image":{"repository":%q,"tag":%q,"pullPolicy":"IfNotPresent"}}}`,
		b.Spec.Image.Repository, b.Spec.Image.Tag)
	matched := 0
	for i := range raw.Items {
		var e kube.KusoEnvironment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &e); err != nil {
			continue
		}
		if e.Spec.Service != b.Spec.Service {
			continue
		}
		// When the build's branch is set (every webhook-triggered build
		// has it), only promote envs that match. When the env has no
		// branch (legacy CRs), promote anyway so production gets the
		// tag — this preserves the TS server's "production gets every
		// build" behaviour while extending it to preview envs whose
		// branch matches the PR head ref.
		if b.Spec.Branch != "" && e.Spec.Branch != "" && e.Spec.Branch != b.Spec.Branch {
			continue
		}
		if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(p.Svc.Namespace).
			Patch(ctx, e.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("patch env %s: %w", e.Name, err)
		}
		matched++
		p.logger().Info("build promoted", "env", e.Name, "tag", b.Spec.Image.Tag)
	}
	if matched == 0 {
		p.logger().Warn("build succeeded but no env matched for promotion",
			"service", b.Spec.Service, "branch", b.Spec.Branch, "tag", b.Spec.Image.Tag)
	}
	return nil
}

// asUnstructured is a small helper to build the unstructured shape for
// writing builds in tests.
func asUnstructured(b *kube.KusoBuild) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(b)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRBuilds.GroupVersion().WithKind("KusoBuild"))
	return u, nil
}

// Used by tests under the same package to build seed objects without
// re-importing kube internals.
var _ = asUnstructured
