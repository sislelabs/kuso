package db

import "testing"

func TestMaxOpenConns(t *testing.T) {
	tests := []struct {
		name, env string
		want      int
	}{
		// An install that sets nothing must size its pool exactly as it did
		// before the override existed — this knob is opt-in tuning, not a
		// behaviour change.
		{"DefaultUnchanged", "", defaultMaxOpenConns},
		{"Override", "60", 60},
		// The important ones: a typo in a tuning env var must never take the
		// control plane down at boot. Every unparseable / nonsensical form
		// falls back to the default.
		{"BadValuesFallBack/abc", "abc", defaultMaxOpenConns},
		{"BadValuesFallBack/zero", "0", defaultMaxOpenConns},
		{"BadValuesFallBack/negative", "-5", defaultMaxOpenConns},
		{"BadValuesFallBack/float", "12.5", defaultMaxOpenConns},
		{"BadValuesFallBack/blank", "  ", defaultMaxOpenConns},
		{"BadValuesFallBack/exponent", "1e3", defaultMaxOpenConns},
		// The deadlock case. A pool of 1 serialises every query, and any code
		// path holding a connection while acquiring a second — the migration
		// runner takes a dedicated *sql.Conn for its advisory lock and then
		// queries — would hang forever.
		{"ClampsTooSmall", "1", minMaxOpenConns},
		// Values injected via YAML env blocks routinely carry a trailing
		// newline or space.
		{"TrimsWhitespace", "  40\n", 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KUSO_DB_MAX_CONNS", tt.env)
			if got := maxOpenConns(); got != tt.want {
				t.Errorf("maxOpenConns() with %q = %d, want %d", tt.env, got, tt.want)
			}
		})
	}
}

func TestIdleConnsFor(t *testing.T) {
	tests := []struct {
		maxOpen, want int
		why           string
	}{
		{25, 10, "default pool keeps ~40% warm (was a fixed 5)"},
		{100, 40, "large pool scales proportionally"},
		{5, 5, "small pool: floor applies, idle == max"},
		{2, 2, "idle never exceeds maxOpen"},
	}
	for _, tt := range tests {
		got := idleConnsFor(tt.maxOpen)
		if got != tt.want {
			t.Errorf("idleConnsFor(%d) = %d, want %d (%s)", tt.maxOpen, got, tt.want, tt.why)
		}
		// The invariant that matters to database/sql: an idle cap above
		// the open cap is silently truncated by the stdlib and signals a
		// sizing bug on our side.
		if got > tt.maxOpen {
			t.Errorf("idleConnsFor(%d) = %d exceeds maxOpen", tt.maxOpen, got)
		}
	}
}
