package vulnscan

import "testing"

func TestFindingOrigin(t *testing.T) {
	cases := []struct{ typ, path, want string }{
		{"ubuntu", "", OriginOS},
		{"alpine", "", OriginOS},
		{"node-pkg", "", ""},
		{"node-pkg", "app/node_modules/.pnpm/tar@7.5.1/node_modules/tar/package.json", OriginApp},
		{"node-pkg", "usr/src/app/node_modules/glob/package.json", OriginApp},
		{"node-pkg", "root/.cache/node/corepack/pnpm/10.20.0/dist/node_modules/tar/package.json", OriginImage},
		{"node-pkg", "nix/store/abc-nodejs-22.11.0/lib/node_modules/npm/node_modules/glob/package.json", OriginImage},
		{"node-pkg", "/usr/local/lib/node_modules/npm/node_modules/tar/package.json", OriginImage},
		{"node-pkg", "home/node/.cache/pnpm/tar/package.json", OriginImage},
		{"gobinary", "usr/local/go/bin/go", OriginImage},
		{"gobinary", "app/server", OriginApp},
		{"python-pkg", "usr/local/lib/python3.12/site-packages/pip-24.0.dist-info/METADATA", OriginImage},
		{"python-pkg", "app/.venv/lib/python3.12/site-packages/requests-2.0.dist-info/METADATA", OriginApp},
	}
	for _, c := range cases {
		if got := FindingOrigin(c.typ, c.path); got != c.want {
			t.Errorf("FindingOrigin(%q, %q) = %q, want %q", c.typ, c.path, got, c.want)
		}
	}
}
