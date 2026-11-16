package handlers

import (
	"reflect"
	"testing"
)

func TestTraefikServiceEnv(t *testing.T) {
	cases := map[string]string{
		"kuso-ja-web-junior-accelerator-web-production-80@kubernetes": "kuso-ja-web-junior-accelerator-web-production",
		"kuso-tickero-api-production-http@kubernetes":                 "kuso-tickero-api-production",
	}
	for in, want := range cases {
		got, ok := traefikServiceEnv(in)
		if !ok || got != want {
			t.Errorf("traefikServiceEnv(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"api@internal", "noport@kubernetes", ""} {
		if got, ok := traefikServiceEnv(in); ok {
			t.Errorf("traefikServiceEnv(%q) = %q, want rejected", in, got)
		}
	}
}

func TestSumVisibleTrafficOnlyCountsVisibleProjects(t *testing.T) {
	series := map[string][][2]float64{
		"kuso-a-web-production-80@kubernetes":      {{100, 1}, {700, 2}},
		"kuso-a-web-staging-80@kubernetes":         {{100, 0.5}},
		"kuso-secret-api-production-80@kubernetes": {{100, 10}, {700, 10}},
		"dashboard@internal":                       {{100, 99}},
	}
	envProject := map[string]string{
		"kuso-a-web-production": "a",
		"kuso-a-web-staging":    "a",
	}
	got := sumVisibleTraffic(series, envProject)
	want := [][2]float64{{100, 90}, {700, 120}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
