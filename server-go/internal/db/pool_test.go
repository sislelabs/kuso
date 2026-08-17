package db

import "testing"

// TestMaxOpenConns_DefaultUnchanged pins the historical value. An install
// that sets nothing must size its pool exactly as it did before the
// override existed — this knob is opt-in tuning, not a behaviour change.
func TestMaxOpenConns_DefaultUnchanged(t *testing.T) {
	t.Setenv("KUSO_DB_MAX_CONNS", "")
	if got := maxOpenConns(); got != defaultMaxOpenConns {
		t.Errorf("maxOpenConns() with unset env = %d, want %d", got, defaultMaxOpenConns)
	}
}

func TestMaxOpenConns_Override(t *testing.T) {
	t.Setenv("KUSO_DB_MAX_CONNS", "60")
	if got := maxOpenConns(); got != 60 {
		t.Errorf("maxOpenConns() = %d, want 60", got)
	}
}

// TestMaxOpenConns_BadValuesFallBack is the important one: a typo in a
// tuning env var must never take the control plane down at boot. Every
// unparseable / nonsensical form falls back to the default.
func TestMaxOpenConns_BadValuesFallBack(t *testing.T) {
	for _, v := range []string{"abc", "0", "-5", "12.5", "  ", "1e3"} {
		t.Setenv("KUSO_DB_MAX_CONNS", v)
		if got := maxOpenConns(); got != defaultMaxOpenConns {
			t.Errorf("maxOpenConns() with %q = %d, want fallback %d", v, got, defaultMaxOpenConns)
		}
	}
}

// TestMaxOpenConns_ClampsTooSmall guards the deadlock case. A pool of 1
// serialises every query, and any code path holding a connection while
// acquiring a second — the migration runner takes a dedicated *sql.Conn
// for its advisory lock and then queries — would hang forever.
func TestMaxOpenConns_ClampsTooSmall(t *testing.T) {
	t.Setenv("KUSO_DB_MAX_CONNS", "1")
	if got := maxOpenConns(); got < minMaxOpenConns {
		t.Errorf("maxOpenConns() = %d, want clamped to >= %d", got, minMaxOpenConns)
	}
}

// TestMaxOpenConns_TrimsWhitespace — values injected via YAML env blocks
// routinely carry a trailing newline or space.
func TestMaxOpenConns_TrimsWhitespace(t *testing.T) {
	t.Setenv("KUSO_DB_MAX_CONNS", "  40\n")
	if got := maxOpenConns(); got != 40 {
		t.Errorf("maxOpenConns() = %d, want 40", got)
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
