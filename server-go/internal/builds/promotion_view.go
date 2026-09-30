package builds

import (
	"context"
	"fmt"

	"kuso/server/internal/kube"
)

// Exported annotation keys the build-summary API reads.
const (
	AnnReleaseJob     = annReleaseJob
	AnnReleaseLogTail = annReleaseLogTail
)

// PromotionIndex answers "where is this build live, and why isn't it"
// for one service's builds, from a single env list.
type PromotionIndex struct {
	envs          []kube.KusoEnvironment
	fqn           string
	defaultBranch string
	live          map[string][]string
}

// PromotionIndex reads project/service's envs once. A build is live on
// an env when that env's promoted-build annotation names it — the stamp
// promotion and rollback write — not when image tags happen to match.
func (s *Service) PromotionIndex(ctx context.Context, project, service string) (*PromotionIndex, error) {
	ns := s.nsFor(ctx, project)
	envs, err := s.serviceEnvs(ctx, ns, project, service)
	if err != nil {
		return nil, err
	}
	fqn := project + "-" + service
	x := &PromotionIndex{envs: envs, fqn: fqn, defaultBranch: s.defaultBranchOf(ctx, project), live: map[string][]string{}}
	for i := range envs {
		if b := envs[i].Annotations[annPromotedBuild]; b != "" {
			x.live[b] = append(x.live[b], envGroupName(&envs[i], fqn))
		}
	}
	return x, nil
}

// LiveEnvs lists the env groups currently running build (nil when none).
func (x *PromotionIndex) LiveEnvs(build string) []string {
	if x == nil {
		return nil
	}
	return x.live[build]
}

// NotPromotedReason explains why a green build of branch reached no env,
// or "" when some env tracks the branch (it was promoted, or superseded).
func (x *PromotionIndex) NotPromotedReason(branch string) string {
	if x == nil {
		return ""
	}
	for i := range x.envs {
		if promotionBranchMatches(branch, x.envs[i].Spec.Branch, x.defaultBranch) {
			return ""
		}
	}
	return fmt.Sprintf("no environment deploys branch %q", branch)
}

// WaitingFor names what a not-yet-started build is waiting on beyond the
// build queue, or "".
func WaitingFor(b *kube.KusoBuild) string {
	if b != nil && b.Annotations[annCIGate] == ciGateWaiting {
		return "GitHub CI checks"
	}
	return ""
}
