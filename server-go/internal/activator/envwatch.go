package activator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

// envSource feeds the host index from a watch-backed informer, so steady
// state costs no LIST: a live cluster-wide env LIST every hostIndexTTL
// per replica was ~20 KB per env of apiserver encode (SCL-1). Until the
// informer syncs, and for good once a watch is refused (the activator
// ClusterRole is applied by hand, so a release can land before `watch`
// is granted), it falls back to the live LIST.
type envSource struct {
	live   func(ctx context.Context) ([]kube.KusoEnvironment, error)
	inf    cache.SharedIndexInformer
	denied atomic.Bool
}

func (s *envSource) list(ctx context.Context) ([]kube.KusoEnvironment, error) {
	if s.inf == nil || s.denied.Load() || !s.inf.HasSynced() {
		return s.live(ctx)
	}
	objs := s.inf.GetStore().List()
	out := make([]kube.KusoEnvironment, 0, len(objs))
	for _, o := range objs {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		var e kube.KusoEnvironment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &e); err != nil {
			return nil, fmt.Errorf("activator: decode cached env %s/%s: %w", u.GetNamespace(), u.GetName(), err)
		}
		out = append(out, e)
	}
	return out, nil
}

// startEnvSource starts the env informer and returns a source whose list
// serves from it. The informer stops when ctx ends or the watch is
// forbidden.
func startEnvSource(ctx context.Context, dyn dynamic.Interface, live func(context.Context) ([]kube.KusoEnvironment, error), logger *slog.Logger) *envSource {
	s := &envSource{live: live}
	if dyn == nil {
		return s
	}
	gi := dynamicinformer.NewFilteredDynamicInformer(dyn, kube.GVREnvironments, metav1.NamespaceAll, 0, cache.Indexers{}, nil)
	inf := gi.Informer()
	_ = inf.SetTransform(slimEnvForHostIndex)
	ictx, cancel := context.WithCancel(ctx)
	var once sync.Once
	_ = inf.SetWatchErrorHandlerWithContext(func(_ context.Context, _ *cache.Reflector, err error) {
		if !apierrors.IsForbidden(err) {
			return
		}
		once.Do(func() {
			s.denied.Store(true)
			logger.Warn("activator: env watch forbidden; falling back to a live LIST per refresh. Apply deploy/kuso-activator.yaml (kusoenvironments needs the watch verb) and restart the activator", "err", err)
			cancel()
		})
	})
	s.inf = inf
	go inf.RunWithContext(ictx)
	return s
}

// slimEnvForHostIndex keeps only what buildHostIndex reads. The full
// object carries status.deployedRelease.manifest and the whole spec,
// which at a thousand envs would be most of the activator's memory.
func slimEnvForHostIndex(obj any) (any, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return obj, nil
	}
	meta := map[string]any{
		"name":            u.GetName(),
		"namespace":       u.GetNamespace(),
		"resourceVersion": u.GetResourceVersion(),
		"uid":             string(u.GetUID()),
	}
	if v, ok := u.GetAnnotations()[scaledown.LastActivityAnnotation]; ok {
		meta["annotations"] = map[string]any{scaledown.LastActivityAnnotation: v}
	}
	spec := map[string]any{}
	if src, ok := u.Object["spec"].(map[string]any); ok {
		for _, k := range []string{"host", "additionalHosts", "wildcardDomains", "internal", "runtime", "stopped", "sleep"} {
			if v, ok := src[k]; ok {
				spec[k] = v
			}
		}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": u.GetAPIVersion(),
		"kind":       u.GetKind(),
		"metadata":   meta,
		"spec":       spec,
	}}, nil
}
