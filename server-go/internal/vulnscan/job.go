package vulnscan

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// CachePVC holds the Trivy DB across scans.
	CachePVC = "kuso-trivy-cache"
	// cachePVCSize: the vuln DB is ~1GB unpacked, the Java DB more when
	// a scanned image has jars.
	cachePVCSize = "2Gi"

	jobDeadlineSeconds = 600
	jobTTLSeconds      = 3600
	scanUID            = 65532

	labelScanID = "kuso.sislelabs.com/vulnscan-id"
	appName     = "kuso-vulnscan"
	container   = "trivy"
)

// scanScript runs Trivy and prints the framed output decodeOutput reads.
// Every input arrives through env (IMAGE, TRIVY_*), never interpolated
// into the script. `trivy version` is best-effort decoration (DB date).
const scanScript = `set -eu
trivy image --scanners vuln --format json --quiet --image-src remote -o /tmp/r.json "$IMAGE"
v=$(trivy version --format json 2>/dev/null | base64 | tr -d '\n' || true)
echo "` + markerVersion + `$v"
echo "` + markerReportBegin + `"
gzip -c /tmp/r.json | base64
echo "` + markerReportEnd + `"
`

// JobName is the scan Job for a VulnScan row: idempotent, so a worker
// restarted mid-scan adopts the Job instead of starting another.
func JobName(scanID int64) string { return "kuso-vulnscan-" + strconv.FormatInt(scanID, 10) }

func authSecretName(scanID int64) string { return JobName(scanID) + "-auth" }

// jobSpec is what buildJob needs to know about one scan.
type jobSpec struct {
	ScanID    int64
	Namespace string
	ImageRef  string
	// Insecure: the image's registry is plain HTTP (the in-cluster one).
	Insecure bool
	// WithAuth mounts the per-Job docker config Secret.
	WithAuth bool
}

// buildJob renders the scan Job. It deliberately carries no project
// label (project NetworkPolicies select on it and would cut registry and
// DB egress) and runs under the namespace's default ServiceAccount with
// no token.
func buildJob(s jobSpec) *batchv1.Job {
	name := JobName(s.ScanID)
	labels := map[string]string{
		"app.kubernetes.io/name":       appName,
		"app.kubernetes.io/managed-by": "kuso-server",
		labelScanID:                    strconv.FormatInt(s.ScanID, 10),
	}
	env := []corev1.EnvVar{
		{Name: "IMAGE", Value: s.ImageRef},
		{Name: "HOME", Value: "/tmp"},
		{Name: "TRIVY_CACHE_DIR", Value: "/cache"},
		{Name: "TRIVY_TIMEOUT", Value: "9m"},
		{Name: "TRIVY_NO_PROGRESS", Value: "true"},
		{Name: "TRIVY_DISABLE_TELEMETRY", Value: "true"},
	}
	if s.Insecure {
		env = append(env, corev1.EnvVar{Name: "TRIVY_INSECURE", Value: "true"})
	}
	mounts := []corev1.VolumeMount{
		{Name: "cache", MountPath: "/cache"},
		{Name: "tmp", MountPath: "/tmp"},
	}
	volumes := []corev1.Volume{
		{Name: "cache", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: CachePVC}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if s.WithAuth {
		// go-containerregistry's default keychain reads
		// $DOCKER_CONFIG/config.json and only offers each entry to its
		// own host.
		env = append(env, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: "/docker"})
		mounts = append(mounts, corev1.VolumeMount{Name: "auth", MountPath: "/docker", ReadOnly: true})
		volumes = append(volumes, corev1.Volume{Name: "auth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: authSecretName(s.ScanID),
			Items:      []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}},
		}}})
	}
	yes, no := true, false
	uid := int64(scanUID)
	backoff := int32(0)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   ptr(int64(jobDeadlineSeconds)),
			TTLSecondsAfterFinished: ptr(int32(jobTTLSeconds)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &no,
					EnableServiceLinks:           &no,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &yes,
						RunAsUser:      &uid,
						RunAsGroup:     &uid,
						FSGroup:        &uid,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
							Weight: 100,
							Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key: "node-role.kubernetes.io/control-plane", Operator: corev1.NodeSelectorOpDoesNotExist,
							}}},
						}},
					}},
					Containers: []corev1.Container{{
						Name:    container,
						Image:   TrivyImage,
						Command: []string{"sh", "-c", scanScript},
						Env:     env,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &no,
							ReadOnlyRootFilesystem:   &yes,
							RunAsNonRoot:             &yes,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
						VolumeMounts: mounts,
					}},
					Volumes: volumes,
				},
			},
		},
	}
}

// buildAuthSecret is the per-Job docker config, owned by the Job so it
// is garbage-collected with it.
func buildAuthSecret(job *batchv1.Job, scanID int64, config []byte) *corev1.Secret {
	ctrl := false
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      authSecretName(scanID),
			Namespace: job.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/name": appName, "app.kubernetes.io/managed-by": "kuso-server"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &ctrl,
			}},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: config},
	}
}

func buildCachePVC(ns string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: CachePVC, Namespace: ns,
			Labels: map[string]string{"app.kubernetes.io/name": appName, "app.kubernetes.io/managed-by": "kuso-server"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(cachePVCSize)},
			},
		},
	}
}

func ptr[T any](v T) *T { return &v }

// ImageHost returns the registry host of an image reference, applying
// Docker's rule: the first path component is a host only if it has a
// dot or colon, or is localhost; otherwise the image is on Docker Hub.
func ImageHost(ref string) string {
	first, _, found := strings.Cut(ref, "/")
	if !found {
		return "docker.io"
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return "docker.io"
}

// ImageRepo strips the tag and/or digest from an image reference.
func ImageRepo(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndexByte(ref, '/')
	if colon := strings.LastIndexByte(ref, ':'); colon > slash {
		ref = ref[:colon]
	}
	return ref
}

// dockerHubKeys are the spellings Docker Hub credentials appear under.
var dockerHubKeys = map[string]bool{"docker.io": true, "index.docker.io": true, "registry-1.docker.io": true}

// normalizeRegistryKey reduces a dockerconfigjson auths key
// ("https://ghcr.io", "https://index.docker.io/v1/") to a bare host.
func normalizeRegistryKey(k string) string {
	k = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(k), "https://"), "http://")
	k, _, _ = strings.Cut(k, "/")
	if dockerHubKeys[k] {
		return "docker.io"
	}
	return k
}

// hostDockerConfig returns a docker config.json holding ONLY host's
// credentials from a dockerconfigjson payload, and false when it has
// none for host. Credentials for any other registry never leave here.
func hostDockerConfig(dockerconfigjson []byte, host string) ([]byte, bool, error) {
	var in struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(dockerconfigjson, &in); err != nil {
		return nil, false, fmt.Errorf("parse dockerconfigjson: %w", err)
	}
	want := normalizeRegistryKey(host)
	for k, v := range in.Auths {
		if normalizeRegistryKey(k) != want {
			continue
		}
		key := host
		if want == "docker.io" {
			key = "https://index.docker.io/v1/" // the key the keychain looks up for Hub
		}
		out, err := json.Marshal(map[string]map[string]json.RawMessage{"auths": {key: v}})
		if err != nil {
			return nil, false, fmt.Errorf("encode docker config: %w", err)
		}
		return out, true, nil
	}
	return nil, false, nil
}
