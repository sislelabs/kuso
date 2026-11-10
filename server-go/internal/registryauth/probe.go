package registryauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	probeLabel     = "kuso.sislelabs.com/registry-auth-probe"
	annProbeNode   = "kuso.sislelabs.com/registry-auth-node"
	annProbeMarker = "kuso.sislelabs.com/registry-auth-marker"
	probeImage     = "busybox:1.38"
	// MarkerPrefix is the comment line install.sh and the join bootstrap
	// append to /etc/rancher/k3s/registries.yaml next to the credential.
	MarkerPrefix = "# kuso-registry-auth: "
)

// Node probe states.
const (
	NodeOK        = "ok"
	NodeMissing   = "missing"
	NodePending   = "pending"
	NodeStale     = "stale"
	NodeNotProbed = "not-probed"
)

type NodeState struct {
	Node  string `json:"node"`
	State string `json:"state"`
}

// Status is the readiness report behind GET /api/admin/registry-auth.
type Status struct {
	Configured bool        `json:"configured"`
	Enabled    bool        `json:"enabled"`
	Marker     string      `json:"marker,omitempty"`
	Nodes      []NodeState `json:"nodes"`
	// ReadyToEnable: a credential exists and every node's registries.yaml
	// was last probed carrying it. It does not prove k3s was restarted
	// after the edit; the documented procedure restarts it.
	ReadyToEnable bool `json:"readyToEnable"`
}

func probeJobName(node string) string {
	sum := sha256.Sum256([]byte(node))
	return "kuso-registry-auth-probe-" + hex.EncodeToString(sum[:5])
}

// ProbeNodes starts one Job per node that checks the node's
// registries.yaml for the current credential marker. Results are read by
// Status; a Job that cannot be created (RBAC, Pod Security) leaves that
// node not-probed, which keeps ReadyToEnable false.
func (s *Store) ProbeNodes(ctx context.Context) error {
	c, err := s.Get(ctx)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("%w: no credential minted yet", ErrInvalid)
	}
	nodes, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("registry auth: list nodes: %w", err)
	}
	jobs := s.Kube.Clientset.BatchV1().Jobs(s.ns())
	bg := metav1.DeletePropagationBackground
	var errs []error
	for _, n := range nodes.Items {
		name := probeJobName(n.Name)
		if err := jobs.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("%s: %w", n.Name, err))
			continue
		}
		if _, err := jobs.Create(ctx, probeJob(s.ns(), name, n.Name, c.Marker()), metav1.CreateOptions{}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", n.Name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("registry auth: probe: %v", errs)
	}
	return nil
}

func probeJob(ns, name, node, marker string) *batchv1.Job {
	zero, ttl := int32(0), int32(3600)
	f, root := false, int64(0)
	hostDir := corev1.HostPathDirectory
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels:      map[string]string{probeLabel: "true"},
			Annotations: map[string]string{annProbeNode: node, annProbeMarker: marker},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &zero,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{probeLabel: "true"}},
				Spec: corev1.PodSpec{
					NodeName:                     node,
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &f,
					Tolerations:                  []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					Containers: []corev1.Container{{
						Name:  "probe",
						Image: probeImage,
						// registries.yaml is root:0600 on k3s nodes. Root
						// owns it, so no DAC capability is needed to read.
						Command: []string{"sh", "-c", `grep -qxF "$MARKER" /host/registries.yaml`},
						Env:     []corev1.EnvVar{{Name: "MARKER", Value: MarkerPrefix + marker}},
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:                &root,
							AllowPrivilegeEscalation: &f,
							ReadOnlyRootFilesystem:   ptr(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "k3s", MountPath: "/host", ReadOnly: true}},
					}},
					Volumes: []corev1.Volume{{Name: "k3s", VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{Path: "/etc/rancher/k3s", Type: &hostDir},
					}}},
				},
			},
		},
	}
}

func ptr[T any](v T) *T { return &v }

// Status reports the credential and per-node probe results.
func (s *Store) Status(ctx context.Context) (*Status, error) {
	c, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	st := &Status{Nodes: []NodeState{}}
	if c != nil {
		st.Configured, st.Enabled, st.Marker = true, c.Enabled, c.Marker()
	}
	nodes, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("registry auth: list nodes: %w", err)
	}
	jobs, err := s.Kube.Clientset.BatchV1().Jobs(s.ns()).List(ctx, metav1.ListOptions{LabelSelector: probeLabel + "=true"})
	if err != nil {
		return nil, fmt.Errorf("registry auth: list probe jobs: %w", err)
	}
	byNode := map[string]*batchv1.Job{}
	for i := range jobs.Items {
		byNode[jobs.Items[i].Annotations[annProbeNode]] = &jobs.Items[i]
	}
	allOK := st.Configured && len(nodes.Items) > 0
	for _, n := range nodes.Items {
		state := nodeState(byNode[n.Name], st.Marker)
		if state != NodeOK {
			allOK = false
		}
		st.Nodes = append(st.Nodes, NodeState{Node: n.Name, State: state})
	}
	sort.Slice(st.Nodes, func(i, j int) bool { return st.Nodes[i].Node < st.Nodes[j].Node })
	st.ReadyToEnable = allOK
	return st, nil
}

func nodeState(j *batchv1.Job, marker string) string {
	switch {
	case j == nil:
		return NodeNotProbed
	case j.Annotations[annProbeMarker] != marker:
		return NodeStale
	case j.Status.Succeeded > 0:
		return NodeOK
	case j.Status.Failed > 0:
		return NodeMissing
	default:
		return NodePending
	}
}
