package handlers

import (
	"reflect"
	"testing"
)

func TestKusoNodeLabels_PrefixesBareKeys(t *testing.T) {
	got := kusoNodeLabels(map[string]string{
		"region":                  "eu-west",
		"kuso.sislelabs.com/tier": "db",
		"example.com/rack":        "r1",
	})
	want := map[string]string{
		"kuso.sislelabs.com/region": "eu-west",
		"kuso.sislelabs.com/tier":   "db",
		"example.com/rack":          "r1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
