package github

import "testing"

// The import preview shows the framework next to runtime and port, so a
// wrong pick here is what the user sees before they deploy.
func TestDetectFromFiles_Framework(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"next beats react and its bundler", map[string]string{
			"package.json": `{"dependencies":{"react":"19","next":"15"},"devDependencies":{"vite":"6"}}`,
		}, "Next.js"},
		{"dev-only vite app", map[string]string{"package.json": `{"devDependencies":{"vite":"6"}}`}, "Vite"},
		{"plain node", map[string]string{"package.json": `{"dependencies":{"lodash":"4"}}`}, "Node.js"},
		{"unreadable package.json", map[string]string{"package.json": ""}, "Node.js"},
		{"django requirements", map[string]string{"requirements.txt": "gunicorn==22\nDjango==5.0\n"}, "Django"},
		{"fastapi in pyproject", map[string]string{"pyproject.toml": "[project]\ndependencies = [\n  \"fastapi>=0.110\",\n]\n"}, "FastAPI"},
		{"flask mentioned only in a comment is not flask", map[string]string{"requirements.txt": "# not flask\nrequests\n"}, "Python"},
		{"go", map[string]string{"go.mod": "module x"}, "Go"},
		{"static", map[string]string{"index.html": "<html>"}, "Static site"},
		{"own Dockerfile names no framework", map[string]string{"Dockerfile": "FROM x", "package.json": `{"dependencies":{"next":"15"}}`}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names := make([]string, 0, len(tc.files))
			for n := range tc.files {
				names = append(names, n)
			}
			got := DetectFromFiles(names, func(rel string) string { return tc.files[rel] })
			if got.Framework != tc.want {
				t.Fatalf("framework = %q (runtime %s), want %q", got.Framework, got.Runtime, tc.want)
			}
		})
	}
}
