package projects

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// volumePVC is one env's existing PVC for a service volume.
type volumePVC struct {
	name         string
	request      resource.Quantity
	storageClass string
	accessModes  []corev1.PersistentVolumeAccessMode
}

const gi = int64(1) << 30

// chartVolumeBytes is the size the env chart renders for sizeGi.
func chartVolumeBytes(sizeGi int) int64 { return int64(max(1, sizeGi)) * gi }

// planVolumeSizes settles each requested volume's size against the PVCs
// that already back it, so the env chart never renders a PVC spec the
// apiserver refuses (which fails the env's whole helm release, BLOB-2):
//
//   - no size given: an existing volume keeps its spec size, and a volume
//     re-added after `volume rm` keeps its retained PVC's size instead of
//     the 1Gi default;
//   - a size below an existing PVC's request is refused (PVCs never shrink);
//   - a size above it is refused unless the PVC's StorageClass allows
//     expansion.
//
// Only volumes whose size actually changes are checked, so an unrelated
// edit is never blocked by an existing mismatch.
func planVolumeSizes(prev []kube.KusoVolume, vols []VolumePatch, pvcs map[string][]volumePVC, expandable func(sc string) (bool, error)) error {
	prevByName := make(map[string]kube.KusoVolume, len(prev))
	for _, v := range prev {
		prevByName[v.Name] = v
	}
	for i := range vols {
		v := &vols[i]
		old, inSpec := prevByName[v.Name]
		if v.SizeGi == 0 {
			if inSpec {
				v.SizeGi = old.SizeGi
			} else {
				for _, p := range pvcs[v.Name] {
					if n := int((p.request.Value() + gi - 1) / gi); n > v.SizeGi {
						v.SizeGi = n
					}
				}
			}
		}
		if inSpec && chartVolumeBytes(v.SizeGi) == chartVolumeBytes(old.SizeGi) {
			continue
		}
		want := chartVolumeBytes(v.SizeGi)
		for _, p := range pvcs[v.Name] {
			have := p.request.Value()
			if want < have {
				return fmt.Errorf("%w: volume %q: %dGi is smaller than its existing disk %s (%s); a volume can only grow",
					ErrConflict, v.Name, max(1, v.SizeGi), p.request.String(), p.name)
			}
			if want == have {
				continue
			}
			ok, err := expandable(p.storageClass)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: volume %q: its disk %s uses StorageClass %q, which cannot be expanded; keep it at %s",
					ErrConflict, v.Name, p.name, p.storageClass, p.request.String())
			}
		}
	}
	return nil
}

// checkVolumeImmutables refuses a storageClass or accessMode change on a
// volume whose PVC already exists. A bound PVC's spec is immutable apart
// from its size, so the env chart's next upgrade would be rejected and every
// later image/env/ingress change on that env would stop landing (OPS-3).
// Only an explicit change is checked: an omitted field keeps the old spec
// value, and an unchanged one is never blocked by an existing mismatch.
func checkVolumeImmutables(prev []kube.KusoVolume, vols []VolumePatch, pvcs map[string][]volumePVC) error {
	prevByName := make(map[string]kube.KusoVolume, len(prev))
	for _, v := range prev {
		prevByName[v.Name] = v
	}
	for _, v := range vols {
		old, inSpec := prevByName[v.Name]
		if v.StorageClass != "" && (!inSpec || v.StorageClass != old.StorageClass) {
			for _, p := range pvcs[v.Name] {
				if p.storageClass != v.StorageClass {
					return fmt.Errorf("%w: volume %q: its disk %s uses StorageClass %q, which cannot be changed on an existing volume; keep %q or use a new volume name",
						ErrConflict, v.Name, p.name, p.storageClass, p.storageClass)
				}
			}
		}
		mode, err := normalizeAccessMode(v.AccessMode)
		if err != nil || mode == "" {
			continue // an invalid mode is refused by the spec validation
		}
		if oldMode, _ := normalizeAccessMode(old.AccessMode); inSpec && mode == oldMode {
			continue
		}
		for _, p := range pvcs[v.Name] {
			if len(p.accessModes) > 0 && string(p.accessModes[0]) != mode {
				return fmt.Errorf("%w: volume %q: its disk %s is %s, and the access mode of an existing volume cannot be changed; keep %s or use a new volume name",
					ErrConflict, v.Name, p.name, p.accessModes[0], p.accessModes[0])
			}
		}
	}
	return nil
}

// singleWriterVolume returns the first volume only one pod can safely
// write ("" access mode is the chart's ReadWriteOnce default), "" if none.
func singleWriterVolume(vols []kube.KusoVolume) string {
	for _, v := range vols {
		switch v.AccessMode {
		case "", "ReadWriteOnce", "ReadWriteOncePod":
			return v.Name
		}
	}
	return ""
}

// validateVolumeScale refuses more than one replica, fixed or autoscaled,
// on a service with a single-writer volume: the extra pods either share
// the disk on one node (concurrent writers on the same files) or hang in
// Multi-Attach errors (BLOB-9).
func validateVolumeScale(vols []kube.KusoVolume, scale *kube.KusoScaleSpec) error {
	if scale == nil {
		return nil
	}
	replicas := max(scale.MinValue(), scale.Max)
	if replicas <= 1 {
		return nil
	}
	if name := singleWriterVolume(vols); name != "" {
		return fmt.Errorf("%w: volume %q is ReadWriteOnce, so only one replica can use it, but the service scales to %d; set min and max replicas to 1 (kuso service set --replicas 1 --max-replicas 1), or use a ReadWriteMany volume",
			ErrInvalid, name, replicas)
	}
	return nil
}

// checkVolumeSizes runs planVolumeSizes against the PVCs of svc's envs,
// filling in sizes on vols.
func (s *Service) checkVolumeSizes(ctx context.Context, ns, project string, svc *kube.KusoService, vols []VolumePatch) error {
	if s.Kube.Clientset == nil {
		return nil
	}
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{labelProject: project})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	mine := map[string]bool{}
	for i := range envs {
		if envs[i].Spec.Service == svc.Name {
			mine[envs[i].Name] = true
		}
	}
	list, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kuso.sislelabs.com/volume," + labelProject + "=" + project,
	})
	if err != nil {
		return fmt.Errorf("list volume PVCs: %w", err)
	}
	pvcs := map[string][]volumePVC{}
	for i := range list.Items {
		p := &list.Items[i]
		if !mine[p.Labels["app.kubernetes.io/instance"]] {
			continue
		}
		vp := volumePVC{name: p.Name, request: p.Spec.Resources.Requests[corev1.ResourceStorage], accessModes: p.Spec.AccessModes}
		if p.Spec.StorageClassName != nil {
			vp.storageClass = *p.Spec.StorageClassName
		}
		name := p.Labels["kuso.sislelabs.com/volume"]
		pvcs[name] = append(pvcs[name], vp)
	}
	if err := checkVolumeImmutables(svc.Spec.Volumes, vols, pvcs); err != nil {
		return err
	}
	return planVolumeSizes(svc.Spec.Volumes, vols, pvcs, func(sc string) (bool, error) {
		if sc == "" {
			return false, nil
		}
		cls, err := s.Kube.Clientset.StorageV1().StorageClasses().Get(ctx, sc, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("get StorageClass %s: %w", sc, err)
		}
		return cls.AllowVolumeExpansion != nil && *cls.AllowVolumeExpansion, nil
	})
}
