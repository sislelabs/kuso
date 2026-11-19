package handlers

import (
	"reflect"
	"testing"
)

func TestFillTrafficMergesPortsAndSkipsUnknownEnvs(t *testing.T) {
	svcs := []insightsService{{Project: "a", Service: "web"}}
	byEnvKey := map[string]int{"kuso-a-web-production": 0}
	tr := insightsTraffic{
		req: map[string][][2]float64{
			"kuso-a-web-production-80@kubernetes":      {{100, 1}, {700, 2}},
			"kuso-a-web-production-9000@kubernetes":    {{100, 1}},
			"kuso-secret-api-production-80@kubernetes": {{100, 50}},
		},
		errs: map[string][][2]float64{
			"kuso-a-web-production-80@kubernetes": {{100, 0.5}},
		},
		p95: map[string][][2]float64{
			"kuso-a-web-production-80@kubernetes":   {{100, 40}},
			"kuso-a-web-production-9000@kubernetes": {{100, 90}},
		},
	}
	fillTraffic(svcs, byEnvKey, tr)
	s := svcs[0]
	if want := [][2]float64{{100, 120}, {700, 120}}; !reflect.DeepEqual(s.Requests, want) {
		t.Errorf("requests = %v, want %v", s.Requests, want)
	}
	if want := [][2]float64{{100, 25}, {700, 0}}; !reflect.DeepEqual(s.ErrorRate, want) {
		t.Errorf("errorRate = %v, want %v", s.ErrorRate, want)
	}
	if want := [][2]float64{{100, 90}}; !reflect.DeepEqual(s.P95, want) {
		t.Errorf("p95 = %v, want %v", s.P95, want)
	}
}
