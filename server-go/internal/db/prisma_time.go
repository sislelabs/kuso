package db

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// prismaTime is a *historical* name from when the schema lived in
// SQLite and Prisma's driver stored DATETIME as int64 unix-millis.
// In v0.9 we moved to Postgres TIMESTAMPTZ; lib/pq writes / reads
// time.Time natively, so prismaTime is now a thin time.Time wrapper.
//
// We keep the type (and the helpers) so the existing call sites
// don't need to be touched. Both Value and Scan deal in time.Time
// at the driver edge; legacy unix-millis input from a serialized
// JSON or a Prisma-era backup is still tolerated by Scan so a one-
// off migration import doesn't blow up.
type prismaTime struct {
	time.Time
}

// Scan implements sql.Scanner. Postgres normally hands us time.Time;
// the int/string branches stay so a Prisma-shaped backup file can be
// imported via tx without a separate normalize step.
func (p *prismaTime) Scan(value any) error {
	if value == nil {
		p.Time = time.Time{}
		return nil
	}
	switch v := value.(type) {
	case time.Time:
		p.Time = v.UTC()
		return nil
	case int64:
		p.Time = time.UnixMilli(v).UTC()
		return nil
	case int:
		p.Time = time.UnixMilli(int64(v)).UTC()
		return nil
	case float64:
		p.Time = time.UnixMilli(int64(v)).UTC()
		return nil
	case []byte:
		return p.scanString(string(v))
	case string:
		return p.scanString(v)
	default:
		return fmt.Errorf("prismaTime: unsupported scan type %T", value)
	}
}

// Value implements driver.Valuer. Returns time.Time so lib/pq writes
// it as a TIMESTAMPTZ. The pre-v0.9 SQLite version returned int64
// millis — that path is gone with the migration.
func (p prismaTime) Value() (driver.Value, error) {
	if p.Time.IsZero() {
		return nil, nil
	}
	return p.Time.UTC(), nil
}

// scanString handles RFC3339 + unix-millis-as-string. Used when a
// driver hands us text instead of a typed value (rare with lib/pq;
// kept as a safety net).
func (p *prismaTime) scanString(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		p.Time = time.Time{}
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		p.Time = t.UTC()
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		p.Time = t.UTC()
		return nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		p.Time = t.UTC()
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		p.Time = time.UnixMilli(n).UTC()
		return nil
	}
	return errors.New("prismaTime: cannot parse string " + s)
}

// nullPrismaTime is the NULL-aware counterpart for nullable
// DateTime columns (lastLogin, emailVerified, etc).
type nullPrismaTime struct {
	Time  time.Time
	Valid bool
}

func (n *nullPrismaTime) Scan(value any) error {
	if value == nil {
		n.Time, n.Valid = time.Time{}, false
		return nil
	}
	var p prismaTime
	if err := p.Scan(value); err != nil {
		return err
	}
	n.Time, n.Valid = p.Time, !p.Time.IsZero()
	return nil
}

func (n nullPrismaTime) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.Time.UTC(), nil
}

// prismaNow returns the current UTC time wrapped in a prismaTime.
// Naming is historical — kept so existing call sites compile.
func prismaNow() prismaTime {
	return prismaTime{Time: time.Now().UTC()}
}

// prismaAt wraps an explicit time.Time. Pass a zero time.Time when
// you actually want NULL — use nullPrismaAt for that.
func prismaAt(t time.Time) prismaTime {
	return prismaTime{Time: t.UTC()}
}

// nullPrismaAt returns a nullPrismaTime that writes NULL when t is
// zero.
func nullPrismaAt(t time.Time) nullPrismaTime {
	return nullPrismaTime{Time: t.UTC(), Valid: !t.IsZero()}
}
