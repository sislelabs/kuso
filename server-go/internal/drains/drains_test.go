package drains

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNormalizeValidates(t *testing.T) {
	cases := []struct {
		name    string
		in      Drain
		wantErr string
	}{
		{"unknown type", Drain{Type: "syslog", URL: "https://x.example"}, "type"},
		{"missing url", Drain{Type: TypeHTTP}, "url"},
		{"non-http scheme", Drain{Type: TypeHTTP, URL: "file:///etc/passwd"}, "scheme"},
		{"localhost", Drain{Type: TypeHTTP, URL: "http://localhost:9000/x"}, "localhost"},
		{"metadata ip", Drain{Type: TypeHTTP, URL: "http://169.254.169.254/latest"}, "reserved"},
		{"reserved header", Drain{Type: TypeHTTP, URL: "https://x.example", Headers: map[string]string{"Host": "evil"}}, "header"},
		{"bad header name", Drain{Type: TypeHTTP, URL: "https://x.example", Headers: map[string]string{"bad name": "v"}}, "header"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Normalize(c.in)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Normalize err=%v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestNormalizeMovesURLCredentialsIntoHeader(t *testing.T) {
	d, err := Normalize(Drain{Type: TypeLoki, URL: "https://123:glc_tok@logs.grafana.net"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.URL, "glc_tok") || strings.Contains(d.URL, "@") {
		t.Fatalf("credentials left in URL: %s", d.URL)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("123:glc_tok"))
	if d.Headers["Authorization"] != want {
		t.Fatalf("Authorization=%q want %q", d.Headers["Authorization"], want)
	}
	if d.Name == "" {
		t.Fatal("name should default")
	}
}

func TestEndpointAppendsProtocolPath(t *testing.T) {
	cases := []struct {
		typ  Type
		in   string
		want string
	}{
		{TypeOTLP, "https://otlp.example.com", "https://otlp.example.com/v1/logs"},
		{TypeOTLP, "https://otlp.example.com/otlp/", "https://otlp.example.com/otlp/v1/logs"},
		{TypeOTLP, "https://otlp.example.com/v1/logs", "https://otlp.example.com/v1/logs"},
		{TypeLoki, "https://loki.example.com", "https://loki.example.com/loki/api/v1/push"},
		{TypeLoki, "https://loki.example.com/loki/api/v1/push", "https://loki.example.com/loki/api/v1/push"},
		{TypeHTTP, "https://hook.example.com/in", "https://hook.example.com/in"},
	}
	for _, c := range cases {
		if got := Endpoint(c.typ, c.in); got != c.want {
			t.Errorf("Endpoint(%s, %s)=%s want %s", c.typ, c.in, got, c.want)
		}
	}
}

func TestMatches(t *testing.T) {
	all := Drain{Enabled: true}
	one := Drain{Enabled: true, Project: "shop"}
	off := Drain{Enabled: false}
	if !all.Matches("anything") || !one.Matches("shop") || one.Matches("blog") || off.Matches("shop") {
		t.Fatal("project scoping wrong")
	}
}
