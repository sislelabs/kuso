package health

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDataVolumeAlert_WarnThenCriticalOnceEach(t *testing.T) {
	steps := []struct {
		pct int
		sev string
	}{
		{60, ""},
		{76, "warn"},
		{80, ""},      // already warned
		{86, "error"}, // escalates once
		{95, ""},
		{72, ""}, // hysteresis band: stays armed
		{69, ""}, // re-arms both
		{90, "error"},
	}
	var warned, critical bool
	for i, s := range steps {
		var sev string
		sev, warned, critical = dataVolumeAlert(s.pct, warned, critical)
		if sev != s.sev {
			t.Fatalf("step %d (%d%%): sev=%q, want %q", i, s.pct, sev, s.sev)
		}
	}
}

func TestDataVolumeTargets(t *testing.T) {
	pod := func(ns, name string, labels map[string]string, phase corev1.PodPhase, claims ...string) corev1.Pod {
		p := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
			Spec:       corev1.PodSpec{NodeName: "n1"},
			Status:     corev1.PodStatus{Phase: phase},
		}
		p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		for _, c := range claims {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: c, VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: c}}})
		}
		return p
	}
	got := dataVolumeTargets([]corev1.Pod{
		pod("alpha", "alpha-db-0", map[string]string{"kuso.sislelabs.com/addon": "alpha-db"}, corev1.PodRunning, "data-alpha-db-0"),
		pod("alpha", "alpha-db-backup-x", map[string]string{"kuso.sislelabs.com/addon": "alpha-db", "kuso.sislelabs.com/role": "backup"}, corev1.PodRunning, "scratch"),
		pod("beta", "beta-db-0", map[string]string{"kuso.sislelabs.com/addon": "beta-db"}, corev1.PodPending, "data-beta-db-0"),
		pod("kuso", "kuso-postgres-1", map[string]string{"cnpg.io/cluster": "kuso-postgres"}, corev1.PodRunning, "kuso-postgres-1"),
		pod("kuso", "other-pg-1", map[string]string{"cnpg.io/cluster": "other"}, corev1.PodRunning, "other-pg-1"),
	})
	want := []dataVolume{
		{Namespace: "alpha", PVC: "data-alpha-db-0", Node: "n1", Owner: "alpha-db"},
		{Namespace: "kuso", PVC: "kuso-postgres-1", Node: "n1", Owner: "control-plane database"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v\nwant %+v", got, want)
	}
}
