package builds

import "testing"

func TestIsBuildSecretLiteral(t *testing.T) {
	cases := []struct {
		name, value string
		want        bool
	}{
		{"DATABASE_URL", "postgres://u:pw@ep-x.neon.tech/db", true},
		{"REDIS_URL", "redis://:hunter2@cache:6379/0", true},
		{"MONGODB_URI", "mongodb+srv://app:s3cr3t@cluster0.mongodb.net/x", true},
		{"AMQP_URL", "amqps://user:pass@mq.example.com/vhost", true},
		{"PG_CONN", "host=db port=5432 user=app password=s3cret dbname=x", true},
		{"MYSQL_CONN", "app:pw@tcp(db:3306)/x?parseTime=true", true},
		{"ALERTS_HOOK", "https://hooks.slack.com/services/T0/B0/xyz", true},
		{"NOTIFY", "https://discord.com/api/webhooks/1/abc", true},
		{"DB_PASS", "x", true},
		{"SMTP_PASS", "x", true},
		{"MYSQL_PWD", "x", true},
		{"DISCORD_WEBHOOK_URL", "https://example.com/anything", true},

		// No credential in the value.
		{"DATABASE_URL", "postgres://db.internal:5432/app", false},
		{"PUBLIC_URL", "https://user@example.com/path", false},
		{"API_BASE", "https://example.com:8443/v1", false},
		{"MAIL_FROM", "Sender <noreply@x.example.com>", false},
		{"COMPASS", "north", false},
		// NEXT_PUBLIC_ is public by construction.
		{"NEXT_PUBLIC_DB", "postgres://u:pw@h/db", false},
		// Name-sensitive keys are withheld instead (stricter), not secret-mounted.
		{"DB_PASSWORD", "x", false},
	}
	for _, c := range cases {
		if got := IsBuildSecretLiteral(c.name, c.value); got != c.want {
			t.Errorf("IsBuildSecretLiteral(%q, %q) = %v, want %v", c.name, c.value, got, c.want)
		}
	}
	if !IsCredentialLiteral("DB_PASSWORD", "x") || !IsCredentialLiteral("DATABASE_URL", "postgres://u:pw@h/db") {
		t.Error("IsCredentialLiteral must cover both rules")
	}
}
