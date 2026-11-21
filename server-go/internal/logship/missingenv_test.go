package logship

import (
	"strings"
	"testing"
)

// matchMissingEnvRegexOnly is matchMissingEnv without the literal
// prefilter: the reference the prefilter must never disagree with.
func matchMissingEnvRegexOnly(line string) string {
	if len(line) < 8 {
		return ""
	}
	for _, re := range missingEnvPatterns {
		if m := re.FindStringSubmatch(line); len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

var missingEnvPositives = []struct{ line, want string }{
	{`KeyError: 'DATABASE_URL'`, "DATABASE_URL"},
	{`Traceback ... KeyError: "SECRET_KEY"`, "SECRET_KEY"},
	{`ReferenceError: API_TOKEN is not defined`, "API_TOKEN"},
	{`Error: Missing env var STRIPE_KEY`, "STRIPE_KEY"},
	{`Missing env: REDIS_URL`, "REDIS_URL"},
	{`Required env var JWT_SECRET`, "JWT_SECRET"},
	{"Missing env\tvar\tS3_BUCKET", "S3_BUCKET"},
	{`panic: missing DB_HOST env var`, "DB_HOST"},
	{"fatal: config: missing\tSMTP_PASS\tenv", "SMTP_PASS"},
	{`envconfig: required key PORT_NUMBER missing value`, "PORT_NUMBER"},
	{`Environment variable OPENAI_KEY is not set`, "OPENAI_KEY"},
	{`NEXTAUTH_SECRET is required but not set`, "NEXTAUTH_SECRET"},
	{"BASE_URL\tis not set", "BASE_URL"},
}

var missingEnvNegatives = []string{
	`2026-11-20T10:00:00.123Z INFO  http: GET /api/projects 200 12ms`,
	`{"level":"info","ts":1732096800.12,"msg":"request completed","path":"/healthz","status":200,"duration_ms":0.4}`,
	`[nodemon] restarting due to changes...`,
	`127.0.0.1 - - [20/Nov/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 612 "-" "kube-probe/1.31"`,
	`  ▲ Next.js 15.0.3 - Local: http://localhost:3000`,
	`keyerror: 'lowercase'`,
	`MISSING ENV VAR FOO`,
	`the variable foo is not set`,
}

func TestMatchMissingEnvPositives(t *testing.T) {
	for _, c := range missingEnvPositives {
		if got := matchMissingEnv(c.line); got != c.want {
			t.Errorf("matchMissingEnv(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// TestMatchMissingEnvPrefilterIsSuperset checks the prefilter never
// rejects a line the regexes would match, including variants of each
// positive embedded in longer lines.
func TestMatchMissingEnvPrefilterIsSuperset(t *testing.T) {
	var corpus []string
	for _, c := range missingEnvPositives {
		corpus = append(corpus, c.line, "prefix "+c.line+" suffix", strings.ToLower(c.line), strings.ToUpper(c.line))
	}
	corpus = append(corpus, missingEnvNegatives...)
	for _, line := range corpus {
		if got, want := matchMissingEnv(line), matchMissingEnvRegexOnly(line); got != want {
			t.Errorf("matchMissingEnv(%q) = %q, regex-only = %q", line, got, want)
		}
	}
	for _, line := range missingEnvNegatives {
		if got := matchMissingEnv(line); got != "" {
			t.Errorf("matchMissingEnv(%q) = %q, want no match", line, got)
		}
	}
}

func BenchmarkMatchMissingEnv(b *testing.B) {
	lines := missingEnvNegatives[:5]
	b.Run("prefilter", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, l := range lines {
				_ = matchMissingEnv(l)
			}
		}
	})
	b.Run("regex-only", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, l := range lines {
				_ = matchMissingEnvRegexOnly(l)
			}
		}
	})
}
