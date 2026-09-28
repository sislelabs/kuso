// Package registrycreds stores private container-registry credentials
// for runtime=image services.
//
// One kubernetes.io/dockerconfigjson Secret per (project, registry host)
// named <project>-regcred-<host-slug>, in the project's execution
// namespace. A service opts in by naming it in spec.image.pullSecret;
// the env/cron/run charts render that as imagePullSecrets. Explicit
// per-service reference (rather than auto-attaching every project
// credential) keeps the propagation path the existing KusoImage copy and
// makes the blast radius of a logout visible: Logout refuses while any
// service still references the credential.
//
// The password is write-only: it lives in the Secret's data and is never
// returned by any method here.
package registrycreds

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

var (
	ErrNotFound = errors.New("registry credential: not found")
	ErrInvalid  = errors.New("registry credential: invalid")
	ErrConflict = errors.New("registry credential: conflict")
)

const (
	labelCredential  = kube.LabelPrefix + "registry-credential"
	annotRegistry    = kube.LabelPrefix + "registry"
	annotUsername    = kube.LabelPrefix + "registry-username"
	annotUpdatedAt   = kube.LabelPrefix + "registry-updated-at"
	dockerHub        = "docker.io"
	dockerHubAuthKey = "https://index.docker.io/v1/"
)

// Credential is the public view of a stored credential. No password.
type Credential struct {
	Registry   string `json:"registry"`
	Username   string `json:"username"`
	SecretName string `json:"secretName"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

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

var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

// NormalizeRegistry turns user input ("https://GHCR.io/", "index.docker.io")
// into the canonical host kuso stores and lists.
func NormalizeRegistry(in string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(in))
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	switch {
	case h == "index.docker.io" || strings.HasPrefix(h, "index.docker.io/"),
		h == "registry-1.docker.io", h == "docker.io", h == "docker.io/":
		return dockerHub, nil
	}
	h = strings.TrimSuffix(h, "/")
	if h == "" || !hostRE.MatchString(h) {
		return "", fmt.Errorf("%w: registry must be a host like ghcr.io or 123.dkr.ecr.eu-west-1.amazonaws.com (got %q)", ErrInvalid, in)
	}
	return h, nil
}

// SecretName is the Secret holding project's credential for host.
func SecretName(project, host string) string {
	slug := strings.NewReplacer(".", "-", ":", "-").Replace(host)
	return project + "-regcred-" + slug
}

func (s *Service) Login(ctx context.Context, project, registry, username, password string) (*Credential, error) {
	host, err := NormalizeRegistry(registry)
	if err != nil {
		return nil, err
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, fmt.Errorf("%w: username and password are required", ErrInvalid)
	}
	authKey := host
	if host == dockerHub {
		authKey = dockerHubAuthKey
	}
	cfg, err := json.Marshal(map[string]any{"auths": map[string]any{authKey: map[string]string{
		"username": username,
		"password": password,
		"auth":     base64.StdEncoding.EncodeToString([]byte(username + ":" + password)),
	}}})
	if err != nil {
		return nil, fmt.Errorf("encode dockerconfigjson: %w", err)
	}
	ns := s.nsFor(ctx, project)
	name := SecretName(project, host)
	now := time.Now().UTC().Format(time.RFC3339)
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				kube.LabelProject:              project,
				labelCredential:                "true",
			},
			Annotations: map[string]string{annotRegistry: host, annotUsername: username, annotUpdatedAt: now},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			return nil, fmt.Errorf("create registry secret %s/%s: %w", ns, name, err)
		}
	case err != nil:
		return nil, fmt.Errorf("get registry secret %s/%s: %w", ns, name, err)
	default:
		// Name collision with a foreign Secret (another project whose name
		// happens to produce the same string, or a hand-made Secret):
		// refuse rather than overwrite it.
		if !ownedBy(existing, project) {
			return nil, fmt.Errorf("%w: secret %s already exists and is not a %s registry credential", ErrConflict, name, project)
		}
		sec.ResourceVersion = existing.ResourceVersion
		if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
			return nil, fmt.Errorf("update registry secret %s/%s: %w", ns, name, err)
		}
	}
	return toCredential(sec), nil
}

func (s *Service) List(ctx context.Context, project string) ([]Credential, error) {
	ns := s.nsFor(ctx, project)
	list, err := s.Kube.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{kube.LabelProject: project, labelCredential: "true"}),
	})
	if err != nil {
		return nil, fmt.Errorf("list registry secrets: %w", err)
	}
	out := make([]Credential, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, *toCredential(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Registry < out[j].Registry })
	return out, nil
}

// Resolve maps a user reference — a registry host or a credential's
// Secret name — to the Secret name, verifying it belongs to project.
// A Secret of another project resolves as ErrNotFound (no existence
// leak).
func (s *Service) Resolve(ctx context.Context, project, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("%w: empty registry credential reference", ErrInvalid)
	}
	name := ref
	if !strings.HasPrefix(ref, project+"-regcred-") {
		host, err := NormalizeRegistry(ref)
		if err != nil {
			return "", fmt.Errorf("%w: no registry credential %q in project %s", ErrNotFound, ref, project)
		}
		name = SecretName(project, host)
	}
	if _, err := s.get(ctx, project, name); err != nil {
		return "", fmt.Errorf("%w: no registry credential %q in project %s (run `kuso registry login`)", ErrNotFound, ref, project)
	}
	return name, nil
}

// Logout deletes project's credential for registry. Refuses with
// ErrConflict while a service still references it: deleting it would
// turn the next pod restart into ImagePullBackOff.
func (s *Service) Logout(ctx context.Context, project, registry string) error {
	host, err := NormalizeRegistry(registry)
	if err != nil {
		return err
	}
	name := SecretName(project, host)
	sec, err := s.get(ctx, project, name)
	if err != nil {
		return err
	}
	users, err := s.referencingServices(ctx, project, name)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("%w: registry credential %s is used by services %s; clear their image pull secret first", ErrConflict, host, strings.Join(users, ", "))
	}
	if err := s.Kube.Clientset.CoreV1().Secrets(sec.Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete registry secret %s: %w", name, err)
	}
	return nil
}

func (s *Service) get(ctx context.Context, project, name string) (*corev1.Secret, error) {
	ns := s.nsFor(ctx, project)
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) || (err == nil && !ownedBy(sec, project)) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("get registry secret %s: %w", name, err)
	}
	return sec, nil
}

func (s *Service) referencingServices(ctx context.Context, project, secretName string) ([]string, error) {
	raw, err := s.Kube.Dynamic.Resource(kube.GVRServices).Namespace(s.nsFor(ctx, project)).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{kube.LabelProject: project}),
	})
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	var out []string
	for i := range raw.Items {
		ps, _, _ := unstructuredString(raw.Items[i].Object, "spec", "image", "pullSecret")
		if ps == secretName {
			out = append(out, strings.TrimPrefix(raw.Items[i].GetName(), project+"-"))
		}
	}
	sort.Strings(out)
	return out, nil
}

func unstructuredString(obj map[string]any, fields ...string) (string, bool, error) {
	cur := any(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur, ok = m[f]
		if !ok {
			return "", false, nil
		}
	}
	s, ok := cur.(string)
	return s, ok, nil
}

func ownedBy(sec *corev1.Secret, project string) bool {
	return sec.Labels[kube.LabelProject] == project && sec.Labels[labelCredential] == "true"
}

func toCredential(sec *corev1.Secret) *Credential {
	return &Credential{
		Registry:   sec.Annotations[annotRegistry],
		Username:   sec.Annotations[annotUsername],
		SecretName: sec.Name,
		UpdatedAt:  sec.Annotations[annotUpdatedAt],
	}
}
