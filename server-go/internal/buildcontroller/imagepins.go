package buildcontroller

// buildImageDigests pins the images kuso picks for a build Job, keyed by
// the exact tag. The buildkit client holds the registry-wide push
// credential and the others run with the source tree or the build env, so
// a re-pushed upstream tag must not reach them. hack/release.sh resolves
// each key's registry digest on every ship and rewrites the value; an
// empty value (a dev build) runs the tag. Keys are literals so release.sh
// can find them; TestBuildImageDigestsCoverDefaults keeps them in step
// with the defaults. A spec naming one of these exact tags (static
// services are created with node:22-alpine) gets the pin too; any other
// image a user names runs as given.
var buildImageDigests = map[string]string{
	"moby/buildkit:v0.34.2":                      "",
	"alpine:3.24":                                "",
	"ghcr.io/sislelabs/kuso-env-detect:v2":       "",
	"ghcr.io/sislelabs/kuso-nixpacks:1.41.0":     "",
	"node:20-alpine":                             "",
	"node:22-alpine":                             "",
	"nginx:1.30-alpine":                          "",
	"buildpacksio/lifecycle:0.21.28":             "",
}

// pinnedImage returns ref@digest when ref is a pinned tag, else ref.
func pinnedImage(ref string) string {
	if d := buildImageDigests[ref]; d != "" {
		return ref + "@" + d
	}
	return ref
}
