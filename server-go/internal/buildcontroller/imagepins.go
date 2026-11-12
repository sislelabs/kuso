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
	"moby/buildkit:v0.34.2":                      "sha256:c5f8862f52499773af85bc07695b2379ef08e8002352b0df2c4864c91d78dabe",
	"alpine:3.24":                                "sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf",
	"ghcr.io/sislelabs/kuso-env-detect:v2":       "sha256:e5c10578b4c862e27afe6370ab26de4cc2a423a0f97db01d712bf630cb7aa9af",
	"ghcr.io/sislelabs/kuso-nixpacks:1.41.0":     "sha256:a1c9edd32806c0335507edf34026eac9827f1dae05929af65607794403c55390",
	"node:20-alpine":                             "sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293",
	"node:22-alpine":                             "sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a",
	"nginx:1.30-alpine":                          "sha256:583b7f2fce1cdb8da5ad5c39ebd3c33b98b87d493190e548bdee1fa8c619c311",
	"buildpacksio/lifecycle:0.21.28":             "sha256:da6997ca53ebfa165a6433db3fefadf651327b13ecb4a896a58c4ac90a29905e",
}

// pinnedImage returns ref@digest when ref is a pinned tag, else ref.
func pinnedImage(ref string) string {
	if d := buildImageDigests[ref]; d != "" {
		return ref + "@" + d
	}
	return ref
}
