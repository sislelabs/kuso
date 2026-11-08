package projects

import "testing"

func TestNewServiceStaticSpecStampsBuilderOnlyForNewStaticServices(t *testing.T) {
	if got := newServiceStaticSpec("static", nil); got == nil || got.BuilderImage != NewStaticBuilderImage {
		t.Fatalf("static, no spec: %+v", got)
	}
	if got := newServiceStaticSpec("static", &ServiceStaticSpec{BuilderImage: "node:18-alpine", OutputDir: "dist"}); got.BuilderImage != "node:18-alpine" || got.OutputDir != "dist" {
		t.Fatalf("explicit builder overwritten: %+v", got)
	}
	if got := newServiceStaticSpec("dockerfile", nil); got != nil {
		t.Fatalf("non-static runtime got a static spec: %+v", got)
	}
}
