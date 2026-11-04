package kusoCli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// withTempCredentials points HOME, the instance list and the credentials
// viper at a scratch dir, restoring the globals afterwards.
func withTempCredentials(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldCreds, oldList, oldNames := credentialsConfig, instanceList, instanceNameList
	oldName, oldInst := currentInstanceName, currentInstance
	t.Cleanup(func() {
		credentialsConfig, instanceList, instanceNameList = oldCreds, oldList, oldNames
		currentInstanceName, currentInstance = oldName, oldInst
	})
	credentialsConfig = viper.NewWithOptions(viper.KeyDelimiter("\x00"))
	instanceList = map[string]Instance{
		"prod.example.com":  {ApiUrl: "https://prod.example.com"},
		"stage.example.com": {ApiUrl: "https://stage.example.com"},
	}
	instanceNameList = []string{"prod.example.com", "stage.example.com"}
	currentInstanceName, currentInstance = "prod.example.com", instanceList["prod.example.com"]
	for name, tok := range map[string]string{"prod.example.com": "prod-token", "stage.example.com": "stage-token"} {
		if err := saveToken(name, tok); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(home, ".kuso", "credentials.yaml")
}

func TestRemoteDeleteRemovesStoredToken(t *testing.T) {
	path := withTempCredentials(t)
	if err := deleteRemote("prod.example.com"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "prod-token") {
		t.Errorf("token still on disk after remote delete:\n%s", body)
	}
	if !strings.Contains(string(body), "stage-token") {
		t.Errorf("other instance's token was lost:\n%s", body)
	}
	if got := credentialsConfig.GetString("prod.example.com"); got != "" {
		t.Errorf("in-memory token survived: %q", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials mode = %v (%v), want 0600", fi.Mode().Perm(), err)
	}
}

func TestLogoutRemovesTokenButKeepsInstance(t *testing.T) {
	path := withTempCredentials(t)
	if err := logout(""); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "prod-token") || !strings.Contains(string(body), "stage-token") {
		t.Errorf("logout of the current instance should drop only its token:\n%s", body)
	}
	if _, ok := instanceList["prod.example.com"]; !ok {
		t.Error("logout must keep the instance entry")
	}
	if err := logout("nope"); err == nil {
		t.Error("logout of an unknown instance should fail")
	}
}
