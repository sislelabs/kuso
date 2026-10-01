package db

import (
	"os"
	"strings"
	"testing"
)

// On a server whose default zone isn't UTC, naive timestamp strings bound
// into TIMESTAMPTZ shifted by the offset: the outbox lease came out hours
// in the past and a second worker re-claimed the row at once.
func TestOpen_PinsSessionToUTC(t *testing.T) {
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var dbName string
	if err := d.QueryRow(`SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`ALTER DATABASE "` + dbName + `" SET timezone = 'Europe/Sofia'`); err != nil {
		t.Skipf("can't change the database default zone: %v", err)
	}
	defer d.Exec(`ALTER DATABASE "` + dbName + `" RESET timezone`)

	d2, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	var tz string
	if err := d2.QueryRow(`SHOW TimeZone`).Scan(&tz); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(tz, "UTC") {
		t.Errorf("session TimeZone = %q, want UTC", tz)
	}
}

func TestWithUTCSession(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"postgres://u:p@h:5432/db?sslmode=disable": "postgres://u:p@h:5432/db?sslmode=disable&timezone=UTC",
		"host=h dbname=db":                         "host=h dbname=db timezone=UTC",
		"postgres://h/db?TimeZone=Europe/Sofia":    "postgres://h/db?TimeZone=Europe/Sofia",
	} {
		if got := withUTCSession(in); got != want {
			t.Errorf("withUTCSession(%q) = %q, want %q", in, got, want)
		}
	}
}
