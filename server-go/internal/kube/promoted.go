package kube

import "time"

// AnnPromotedAt records when an env's current image went live. The build
// poller and rollback stamp it on promotion; runtime=image services stamp
// it when their image is set or changed, since they never pass through a
// build. The UI's "deployed N ago" reads it.
const AnnPromotedAt = "kuso.sislelabs.com/promoted-at"

// AnnPromotedBuild names the build an env currently runs. Written with
// AnnPromotedAt by promotion and rollback.
const AnnPromotedBuild = "kuso.sislelabs.com/promoted-build"

// StampPromotedAt sets AnnPromotedAt on env to now.
func StampPromotedAt(env *KusoEnvironment, now time.Time) {
	if env.Annotations == nil {
		env.Annotations = map[string]string{}
	}
	env.Annotations[AnnPromotedAt] = now.UTC().Format(time.RFC3339Nano)
}
