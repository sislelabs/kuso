package drains

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func sampleLines() []Line {
	t0 := time.Date(2026, 9, 28, 10, 0, 0, 500, time.UTC)
	obs := time.Date(2026, 9, 28, 10, 0, 1, 0, time.UTC)
	return []Line{
		{Ts: t0, Observed: obs, Project: "shop", Service: "web", Env: "shop-web-production", EnvKind: "production", Pod: "web-1", Line: "GET / 200"},
		{Ts: t0.Add(time.Second), Observed: obs, Project: "shop", Service: "worker", Env: "shop-worker-production", EnvKind: "production", Pod: "worker-1", Line: "job done"},
		{Ts: t0.Add(2 * time.Second), Observed: obs, Project: "shop", Service: "web", Env: "shop-web-production", EnvKind: "production", Pod: "web-2", Line: `panic: "boom"`},
	}
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w bytes.Buffer
	if err := json.Compact(&g, got); err != nil {
		t.Fatalf("got is not JSON: %v\n%s", err, got)
	}
	if err := json.Compact(&w, []byte(want)); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if g.String() != w.String() {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", g.String(), w.String())
	}
}

func TestEncodeHTTPGolden(t *testing.T) {
	got, err := Encode(TypeHTTP, sampleLines()[:1])
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, `[
	  {"ts":"2026-09-28T10:00:00.0000005Z","project":"shop","service":"web","env":"shop-web-production","pod":"web-1","stream":"combined","line":"GET / 200"}
	]`)
}

func TestEncodeOTLPGolden(t *testing.T) {
	got, err := Encode(TypeOTLP, sampleLines())
	if err != nil {
		t.Fatal(err)
	}
	// Grouped per (project, service, env): web's two records share one
	// resource, worker gets its own. Resource order follows first sight.
	assertJSONEqual(t, got, `{"resourceLogs":[
	  {"resource":{"attributes":[
	      {"key":"service.name","value":{"stringValue":"shop/web"}},
	      {"key":"kuso.project","value":{"stringValue":"shop"}},
	      {"key":"kuso.service","value":{"stringValue":"web"}},
	      {"key":"kuso.env","value":{"stringValue":"shop-web-production"}},
	      {"key":"deployment.environment","value":{"stringValue":"production"}}]},
	   "scopeLogs":[{"scope":{"name":"kuso"},"logRecords":[
	      {"timeUnixNano":"1790589600000000500","observedTimeUnixNano":"1790589601000000000","body":{"stringValue":"GET / 200"},
	       "attributes":[{"key":"k8s.pod.name","value":{"stringValue":"web-1"}}]},
	      {"timeUnixNano":"1790589602000000500","observedTimeUnixNano":"1790589601000000000","body":{"stringValue":"panic: \"boom\""},
	       "attributes":[{"key":"k8s.pod.name","value":{"stringValue":"web-2"}}]}]}]},
	  {"resource":{"attributes":[
	      {"key":"service.name","value":{"stringValue":"shop/worker"}},
	      {"key":"kuso.project","value":{"stringValue":"shop"}},
	      {"key":"kuso.service","value":{"stringValue":"worker"}},
	      {"key":"kuso.env","value":{"stringValue":"shop-worker-production"}},
	      {"key":"deployment.environment","value":{"stringValue":"production"}}]},
	   "scopeLogs":[{"scope":{"name":"kuso"},"logRecords":[
	      {"timeUnixNano":"1790589601000000500","observedTimeUnixNano":"1790589601000000000","body":{"stringValue":"job done"},
	       "attributes":[{"key":"k8s.pod.name","value":{"stringValue":"worker-1"}}]}]}]}
	]}`)
}

func TestEncodeLokiGolden(t *testing.T) {
	got, err := Encode(TypeLoki, sampleLines())
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, `{"streams":[
	  {"stream":{"env":"shop-web-production","project":"shop","service":"web"},
	   "values":[["1790589600000000500","GET / 200"],["1790589602000000500","panic: \"boom\""]]},
	  {"stream":{"env":"shop-worker-production","project":"shop","service":"worker"},
	   "values":[["1790589601000000500","job done"]]}
	]}`)
}

func TestEncodeUnknownType(t *testing.T) {
	if _, err := Encode("syslog", sampleLines()); err == nil {
		t.Fatal("unknown type must error")
	}
}
