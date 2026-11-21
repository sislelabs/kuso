package kube

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type publicTCPObj struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		IPAllowList struct {
			SourceRange []string `yaml:"sourceRange"`
		} `yaml:"ipAllowList"`
		InFlightConn struct {
			Amount int `yaml:"amount"`
		} `yaml:"inFlightConn"`
		Routes []struct {
			Middlewares []struct {
				Name string `yaml:"name"`
			} `yaml:"middlewares"`
		} `yaml:"routes"`
	} `yaml:"spec"`
}

func publicTCPObjs(t *testing.T, out string) map[string]publicTCPObj {
	t.Helper()
	objs := map[string]publicTCPObj{}
	for _, doc := range strings.Split(out, "\n---\n") {
		var o publicTCPObj
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("decode: %v\n%s", err, doc)
		}
		if o.Kind == "IngressRouteTCP" || o.Kind == "MiddlewareTCP" {
			objs[o.Kind+"/"+o.Metadata.Name] = o
		}
	}
	return objs
}

func routeMiddlewares(t *testing.T, objs map[string]publicTCPObj) []string {
	t.Helper()
	r, ok := objs["IngressRouteTCP/test-addon-public-tcp"]
	if !ok || len(r.Spec.Routes) != 1 {
		t.Fatalf("no single-route IngressRouteTCP in %v", objs)
	}
	var names []string
	for _, m := range r.Spec.Routes[0].Middlewares {
		names = append(names, m.Name)
	}
	return names
}

func TestAddonChart_PublicTCPRestrictions(t *testing.T) {
	t.Parallel()
	on := []string{"publicTCP.enabled=true", "publicTCP.port=30001"}

	t.Run("unset renders no middleware", func(t *testing.T) {
		t.Parallel()
		objs := publicTCPObjs(t, helmTemplateAddon(t, "postgres", on...))
		if len(objs) != 1 {
			t.Fatalf("want only the IngressRouteTCP, got %v", objs)
		}
		if mw := routeMiddlewares(t, objs); mw != nil {
			t.Fatalf("route references middlewares %v", mw)
		}
	})

	t.Run("both set", func(t *testing.T) {
		t.Parallel()
		objs := publicTCPObjs(t, helmTemplateAddon(t, "postgres", append(on,
			"publicTCP.sourceRanges={203.0.113.0/24,2001:db8::/32}",
			"publicTCP.maxConnections=20")...))
		allow := objs["MiddlewareTCP/test-addon-public-tcp-allow"]
		if want := []string{"203.0.113.0/24", "2001:db8::/32"}; !reflect.DeepEqual(allow.Spec.IPAllowList.SourceRange, want) {
			t.Errorf("ipAllowList.sourceRange = %v, want %v", allow.Spec.IPAllowList.SourceRange, want)
		}
		if got := objs["MiddlewareTCP/test-addon-public-tcp-inflight"].Spec.InFlightConn.Amount; got != 20 {
			t.Errorf("inFlightConn.amount = %d, want 20", got)
		}
		want := []string{"test-addon-public-tcp-allow", "test-addon-public-tcp-inflight"}
		if mw := routeMiddlewares(t, objs); !reflect.DeepEqual(mw, want) {
			t.Errorf("route middlewares = %v, want %v", mw, want)
		}
	})

	t.Run("cap only", func(t *testing.T) {
		t.Parallel()
		objs := publicTCPObjs(t, helmTemplateAddon(t, "postgres", append(on, "publicTCP.maxConnections=5")...))
		if _, ok := objs["MiddlewareTCP/test-addon-public-tcp-allow"]; ok {
			t.Error("allow-list rendered without sourceRanges")
		}
		if mw := routeMiddlewares(t, objs); !reflect.DeepEqual(mw, []string{"test-addon-public-tcp-inflight"}) {
			t.Errorf("route middlewares = %v", mw)
		}
	})

	t.Run("no route means no middleware", func(t *testing.T) {
		t.Parallel()
		out := helmTemplateAddon(t, "redpanda", append(on, "publicTCP.sourceRanges={203.0.113.0/24}")...)
		if strings.Contains(out, "MiddlewareTCP") {
			t.Fatal("MiddlewareTCP rendered for a kind with no public route")
		}
	})
}
