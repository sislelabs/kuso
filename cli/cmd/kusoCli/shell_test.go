package kusoCli

import (
	"strings"
	"testing"
)

func TestShellTerminalURL(t *testing.T) {
	got, err := shellTerminalURL("https://kuso.example.com/", "shop", "api", "staging", "", "web")
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://kuso.example.com/ws/projects/shop/services/api/terminal?container=web&env=staging"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --kubectl used to exec against whatever kubeconfig context was
// current, which could be a different cluster.
func TestShellKubectlRequiresContext(t *testing.T) {
	shellContext = ""
	err := runKubectlShell("shop", "api")
	if err == nil || !strings.Contains(err.Error(), "--context") {
		t.Fatalf("want --context requirement, got %v", err)
	}
}
