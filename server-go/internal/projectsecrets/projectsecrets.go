// Package projectsecrets implements project-level secret storage —
// env vars that get auto-attached to every service in the project
// via envFromSecrets. Use case: integrations like Resend, Postmark,
// Stripe, OpenAI — anything every service in the same SaaS needs.
//
// Backed by one kube Secret per project named "<project>-shared".
// Created lazily on first SetKey. The kuso-server pre-populates
// every new env's envFromSecrets list to include the shared secret
// alongside the addon connection secrets.
package projectsecrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"kuso/server/internal/kube"
	"kuso/server/internal/secrets"
)

type Service struct {
	Kube       *kube.Client
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
}

func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// SecretName returns the canonical project-shared Secret name.
// Exported because the projects package needs it to wire into
// envFromSecrets.
func SecretName(project string) string {
	return project + "-shared"
}

var (
	ErrInvalid  = errors.New("projectsecrets: invalid")
	ErrNotFound = errors.New("projectsecrets: not found")
)

// ListKeys returns the keys (no values) currently stored. Empty
// slice when the Secret doesn't exist yet.
func (s *Service) ListKeys(ctx context.Context, project string) ([]string, error) {
	sec, err := s.read(ctx, s.nsFor(ctx, project), SecretName(project))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read shared secret: %w", err)
	}
	out := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		out = append(out, k)
	}
	return out, nil
}

// SetOptions controls SetKey behavior. Force=true bypasses the
// shadow guard (see CheckSharedSetShadow) — use when the caller has
// explicitly accepted that a service-scoped copy of the same key
// will override the new shared value.
type SetOptions struct {
	Force bool
}

// SetKey upserts a single env-var-style entry. Creates the Secret
// when it doesn't exist yet. Returns the number of KusoEnvironments
// whose pods were triggered to roll so the new value reaches them —
// kube's envFrom is evaluated at pod start, so a Secret-only update
// is invisible to already-running pods until they restart.
//
// When any service in the project has the same key in its service-
// scoped Secret, the new shared value would be silently invisible
// (kube's "last source wins" envFrom semantics + the chart mounting
// service-scoped AFTER shared). SetKey refuses with a *ShadowedError
// in that case unless opts.Force is true. Callers usually surface
// the error to the user with a "unset the service-scoped copy or
// pass --force" message — silent shadowing is exactly the trap this
// guard exists to prevent.
func (s *Service) SetKey(ctx context.Context, project, key, value string, opts SetOptions) (rolled int, err error) {
	if key == "" {
		return 0, fmt.Errorf("%w: key required", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	if !opts.Force {
		shadow, _ := secrets.CheckSharedSetShadow(ctx, s.Kube, project, ns, key)
		if shadow != nil {
			return 0, shadow
		}
	}
	name := SecretName(project)
	// Re-read + write under RetryOnConflict so concurrent SetKey/UnsetKey
	// on this project's shared Secret can't clobber each other through a
	// stale resourceVersion. The dependent-env rollout runs once after,
	// outside the loop, since it's idempotent and doesn't touch the Secret.
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		sec, err := s.read(ctx, ns, name)
		if apierrors.IsNotFound(err) {
			// Create fresh.
			fresh := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
					Labels: map[string]string{
						"kuso.sislelabs.com/project": project,
						"kuso.sislelabs.com/role":    "shared-project-envs",
					},
				},
				Data: map[string][]byte{key: []byte(value)},
				Type: corev1.SecretTypeOpaque,
			}
			if _, cerr := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, fresh, metav1.CreateOptions{}); cerr != nil {
				// A racing create won: retry into the read+update path.
				if apierrors.IsAlreadyExists(cerr) {
					return apierrors.NewConflict(corev1.Resource("secrets"), name, cerr)
				}
				return fmt.Errorf("create shared secret: %w", cerr)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("read shared secret: %w", err)
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data[key] = []byte(value)
		if _, uerr := s.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, sec, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update shared secret: %w", uerr)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return s.rollDependentEnvs(ctx, project, ns, name)
}

// UnsetKey removes one entry. No-op (rolled=0) when the key (or the
// whole Secret) doesn't exist — matches the "delete is idempotent"
// expectation for kuso secrets. When a key is actually removed, rolls
// every env that has the shared Secret attached so the removal takes
// effect on already-running pods.
func (s *Service) UnsetKey(ctx context.Context, project, key string) (rolled int, err error) {
	ns := s.nsFor(ctx, project)
	name := SecretName(project)
	// missing tracks whether the key (or whole Secret) was absent, so we
	// can skip the dependent-env rollout — there's nothing to propagate.
	var missing bool
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		sec, err := s.read(ctx, ns, name)
		if apierrors.IsNotFound(err) {
			missing = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("read shared secret: %w", err)
		}
		if sec.Data == nil {
			missing = true
			return nil
		}
		if _, ok := sec.Data[key]; !ok {
			missing = true
			return nil
		}
		missing = false
		delete(sec.Data, key)
		if _, uerr := s.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, sec, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update shared secret: %w", uerr)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if missing {
		return 0, nil
	}
	return s.rollDependentEnvs(ctx, project, ns, name)
}

// rollDependentEnvs bumps spec.secretsRev on every KusoEnvironment in
// the project whose pods consume the named Secret via envFromSecrets.
// The kusoenvironment chart projects spec.secretsRev onto the pod
// template's annotations (kuso.sislelabs.com/secrets-rev), which
// kubelet treats as a template change → rolling restart.
//
// Without this, kube's envFrom semantics leave running pods stale:
// they hold the env they were launched with, even after the backing
// Secret changes. New pods get the new value; existing ones don't.
//
// Best-effort: a patch error on a single env is logged + counted as
// a miss, but doesn't abort the loop or fail the secret write. The
// secret value is the source of truth — partial rollout is a
// recoverable degraded state, while refusing the secret update on a
// rollout failure would leave the cluster with no path forward.
//
// Returns the number of envs successfully rolled.
func (s *Service) rollDependentEnvs(ctx context.Context, project, ns, secretName string) (int, error) {
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
	})
	if err != nil {
		return 0, fmt.Errorf("list envs: %w", err)
	}
	rev := strconv.FormatInt(time.Now().UnixMilli(), 10)
	patch := fmt.Sprintf(`{"spec":{"secretsRev":%q}}`, rev)
	rolled := 0
	for _, e := range envs {
		hasIt := false
		for _, s := range e.Spec.EnvFromSecrets {
			if s == secretName {
				hasIt = true
				break
			}
		}
		if !hasIt {
			continue
		}
		_, perr := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Patch(ctx, e.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
		if perr != nil {
			slog.Warn("projectsecrets: bump secretsRev failed",
				"env", e.Name, "project", project, "err", perr)
			continue
		}
		rolled++
	}
	return rolled, nil
}

func (s *Service) read(ctx context.Context, ns, name string) (*corev1.Secret, error) {
	return s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
}
