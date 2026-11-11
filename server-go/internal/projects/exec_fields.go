package projects

import (
	"slices"
	"strings"

	"kuso/server/internal/kube"
)

// Execution-bearing service fields (SEC-1). Each one picks code that runs
// with the service's env (its secret, addon conns, shared secrets): the
// run command, the image, the release hook, and the build-time command,
// builder images and build args (NIXPACKS_*_CMD among them) that run with
// the build env. Setting one is therefore the same capability as reading
// secret values, and handlers gate it on the project admin role.
//
// Clearing a field back to its default is not gated: the default runs the
// repo's or image's own code, which the field didn't choose.
const (
	ExecFieldRuntime          = "runtime"
	ExecFieldImage            = "image"
	ExecFieldCommand          = "command"
	ExecFieldRelease          = "release"
	ExecFieldFromService      = "fromService"
	ExecFieldBuildCmd         = "static.buildCmd"
	ExecFieldStaticBuilder    = "static.builderImage"
	ExecFieldStaticRuntime    = "static.runtimeImage"
	ExecFieldBuildpackBuilder = "buildpacks.builderImage"
	ExecFieldBuildpackLife    = "buildpacks.lifecycleImage"
	ExecFieldBuildArgs        = "buildArgs"
)

// ExecPatchFields returns the execution-bearing fields req would actually
// change on cur. Values equal to what's stored (a declarative re-send, a
// masked build arg) don't count, so a kuso.yaml re-apply or a settings
// save that leaves them alone isn't gated.
func ExecPatchFields(cur *kube.KusoServiceSpec, req PatchServiceRequest) []string {
	if cur == nil {
		cur = &kube.KusoServiceSpec{}
	}
	var out []string
	if req.Runtime != nil && normRuntime(*req.Runtime) != normRuntime(cur.Runtime) {
		out = append(out, ExecFieldRuntime)
	}
	if req.Image != nil {
		repo := strings.TrimSpace(req.Image.Repository)
		if repo != "" {
			tag := strings.TrimSpace(req.Image.Tag)
			if tag == "" {
				tag = "latest"
			}
			var curRepo, curTag string
			if cur.Image != nil {
				curRepo, curTag = cur.Image.Repository, cur.Image.Tag
			}
			if repo != curRepo || tag != curTag {
				out = append(out, ExecFieldImage)
			}
		}
	}
	if req.Command != nil && len(*req.Command) > 0 && !slices.Equal(*req.Command, cur.Command) {
		out = append(out, ExecFieldCommand)
	}
	if req.Release != nil && !req.Release.Clear && len(req.Release.Command) > 0 {
		var curCmd []string
		if cur.Release != nil {
			curCmd = cur.Release.Command
		}
		if !slices.Equal(req.Release.Command, curCmd) {
			out = append(out, ExecFieldRelease)
		}
	}
	if req.Static != nil {
		var c kube.KusoStaticSpec
		if cur.Static != nil {
			c = *cur.Static
		}
		out = appendIfSet(out, ExecFieldBuildCmd, req.Static.BuildCmd, c.BuildCmd)
		out = appendIfSet(out, ExecFieldStaticBuilder, req.Static.BuilderImage, c.BuilderImage)
		out = appendIfSet(out, ExecFieldStaticRuntime, req.Static.RuntimeImage, c.RuntimeImage)
	}
	if req.Buildpacks != nil {
		var c kube.KusoBuildpacksSpec
		if cur.Buildpacks != nil {
			c = *cur.Buildpacks
		}
		out = appendIfSet(out, ExecFieldBuildpackBuilder, req.Buildpacks.BuilderImage, c.BuilderImage)
		out = appendIfSet(out, ExecFieldBuildpackLife, req.Buildpacks.LifecycleImage, c.LifecycleImage)
	}
	if req.BuildArgs != nil {
		for k, v := range *req.BuildArgs {
			if v == EnvMaskSentinel {
				continue // resolved to the stored value by unmaskBuildArgs
			}
			if old, ok := cur.BuildArgs[k]; !ok || old != v {
				out = append(out, ExecFieldBuildArgs)
				break
			}
		}
	}
	return out
}

// ExecCreateFields returns the execution-bearing fields a new service
// would be created with. A plain repo-built service returns none.
func ExecCreateFields(req CreateServiceRequest) []string {
	var out []string
	if req.Image != nil && strings.TrimSpace(req.Image.Repository) != "" {
		out = append(out, ExecFieldImage)
	}
	if len(req.Command) > 0 {
		out = append(out, ExecFieldCommand)
	}
	if req.Release != nil && !req.Release.Clear && len(req.Release.Command) > 0 {
		out = append(out, ExecFieldRelease)
	}
	if strings.TrimSpace(req.FromService) != "" {
		out = append(out, ExecFieldFromService)
	}
	if req.Static != nil {
		out = appendIfSet(out, ExecFieldBuildCmd, req.Static.BuildCmd, "")
		out = appendIfSet(out, ExecFieldStaticBuilder, req.Static.BuilderImage, "")
		out = appendIfSet(out, ExecFieldStaticRuntime, req.Static.RuntimeImage, "")
	}
	if req.Buildpacks != nil {
		out = appendIfSet(out, ExecFieldBuildpackBuilder, req.Buildpacks.BuilderImage, "")
		out = appendIfSet(out, ExecFieldBuildpackLife, req.Buildpacks.LifecycleImage, "")
	}
	if len(req.BuildArgs) > 0 {
		out = append(out, ExecFieldBuildArgs)
	}
	return out
}

func appendIfSet(out []string, field, want, have string) []string {
	if want = strings.TrimSpace(want); want != "" && want != have {
		return append(out, field)
	}
	return out
}

func normRuntime(rt string) string {
	if rt = strings.TrimSpace(rt); rt == "" {
		return "dockerfile"
	}
	return rt
}
