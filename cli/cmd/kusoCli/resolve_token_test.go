package kusoCli

import (
	"testing"

	"github.com/spf13/viper"
)

// C1: KUSO_API_URL pointing at another host must not carry the current
// instance's saved JWT.
func TestResolveToken_EnvURLNeverLeaksCurrentInstanceToken(t *testing.T) {
	origCreds, origList, origName := credentialsConfig, instanceList, currentInstanceName
	t.Cleanup(func() { credentialsConfig, instanceList, currentInstanceName = origCreds, origList, origName })

	credentialsConfig = viper.NewWithOptions(viper.KeyDelimiter("\x00"))
	credentialsConfig.Set("prod", "prod-jwt")
	credentialsConfig.Set("staging", "staging-jwt")
	credentialsConfig.Set("kuso.byhost.example", "host-jwt")
	instanceList = map[string]Instance{
		"prod":    {Name: "prod", ApiUrl: "https://kuso.prod.example"},
		"staging": {Name: "staging", ApiUrl: "https://kuso.staging.example/"},
	}
	currentInstanceName = "prod"

	cases := []struct{ name, envURL, envTok, want string }{
		{"no env url uses current instance", "", "", "prod-jwt"},
		{"KUSO_TOKEN wins", "https://other.example", "env-jwt", "env-jwt"},
		{"foreign host gets no token", "https://other.example", "", ""},
		{"matching saved apiurl", "https://kuso.staging.example", "", "staging-jwt"},
		{"host-keyed credential", "https://kuso.byhost.example/", "", "host-jwt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUSO_API_URL", tc.envURL)
			t.Setenv("KUSO_URL", "")
			t.Setenv("KUSO_TOKEN", tc.envTok)
			if got := resolveToken(); got != tc.want {
				t.Fatalf("resolveToken() = %q, want %q", got, tc.want)
			}
		})
	}
}
