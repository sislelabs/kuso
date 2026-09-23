package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// The chart used to emit volumes[].accessMode verbatim, so the documented
// "RWO" shorthand produced a PVC the apiserver rejects and wedged the env's
// helm release. PatchService now normalises the shorthand and rejects
// anything that isn't a k8s access mode.
func TestPatchService_VolumeAccessMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "", want: ""},
		{in: "RWO", want: "ReadWriteOnce"},
		{in: "rwx", want: "ReadWriteMany"},
		{in: "ROX", want: "ReadOnlyMany"},
		{in: "ReadWriteOnce", want: "ReadWriteOnce"},
		{in: "ReadWriteMany", want: "ReadWriteMany"},
		{in: "ReadOnlyMany", want: "ReadOnlyMany"},
		{in: "ReadWriteOncePod", want: "ReadWriteOncePod"},
		{in: "readwriteonce", want: "ReadWriteOnce"},
		{in: "RWOP", wantErr: true},
		{in: "shared", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			s := fakeService(t,
				seedProject("alpha", kube.KusoProjectSpec{}),
				seedService("alpha", "web", kube.KusoServiceSpec{Port: 8080}),
				seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
			)
			vols := []VolumePatch{{Name: "data", MountPath: "/data", AccessMode: tc.in}}
			_, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Volumes: &vols})
			if tc.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("accessMode %q: err = %v, want ErrInvalid", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("PatchService: %v", err)
			}
			env := envByName(t, s, "alpha", "web")["alpha-web-production"]
			if len(env.Spec.Volumes) != 1 || env.Spec.Volumes[0].AccessMode != tc.want {
				t.Fatalf("env volumes = %+v, want accessMode %q", env.Spec.Volumes, tc.want)
			}
		})
	}
}
