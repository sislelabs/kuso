package db

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// prismaTime adapts the way Prisma's SQLite driver stores DateTime
// columns: the value lives as INTEGER milliseconds-since-epoch, but
// when the schema runs through `prisma migrate diff` we get a `DATETIME`
// column type. modernc.org/sqlite reports the cell as an int64 and our
// `time.Time` scan fails with "unsupported Scan, storing driver.Value
// type int64 into type *time.Time".
//
// Wrapping the field in prismaTime gives sql.Rows.Scan an explicit
// Scanner that handles all four shapes Prisma can emit:
//   - int64 / int  (unix milliseconds, the default)
//   - float64       (millis with fractional part — rare but possible)
//   - string        (RFC3339 / RFC3339Nano — what `prisma migrate diff`
//                    docs claim and what the kuso seed sometimes writes)
//   - []byte        (string variant from older drivers)
type prismaTime struct {
	time.Time
}

// Scan implements sql.Scanner.
func (p *prismaTime) Scan(value any) error {
	if value == nil {
		p.Time = time.Time{}
		return nil
	}
	switch v := value.(type) {
	case int64:
		p.Time = time.UnixMilli(v).UTC()
		return nil
	case int:
		p.Time = time.UnixMilli(int64(v)).UTC()
		return nil
	case float64:
		p.Time = time.UnixMilli(int64(v)).UTC()
		return nil
	case time.Time:
		p.Time = v.UTC()
		return nil
	case []byte:
		return p.scanString(string(v))
	case string:
		return p.scanString(v)
	default:
		return fmt.Errorf("prismaTime: unsupported scan type %T", value)
	}
}

// Value implements driver.Valuer so a prismaTime survives writeback
// through ExecContext as int64 milliseconds — the shape Prisma reads.
func (p prismaTime) Value() (driver.Value, error) {
	if p.Time.IsZero() {
		return nil, nil
	}
	return p.Time.UnixMilli(), nil
}

// scanString handles RFC3339 and Unix-millis-as-string.
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

// nullPrismaTime is the NULL-aware counterpart for nullable DateTime
// columns (e.g. lastLogin, emailVerified).
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
	return n.Time.UnixMilli(), nil
}

// prismaNow returns the current UTC time wrapped in a prismaTime so it
// writes through Exec as int64 milliseconds — the format Prisma reads
// back. Use this everywhere a write previously used `time.Now().UTC()`
// for a DateTime column on the kuso SQLite.
func prismaNow() prismaTime {
	return prismaTime{Time: time.Now().UTC()}
}

// prismaAt wraps an explicit time.Time into a prismaTime for writes.
// Pass a zero time.Time to write NULL through nullPrismaTime — this
// helper is for non-null columns only.
func prismaAt(t time.Time) prismaTime {
	return prismaTime{Time: t.UTC()}
}

// nullPrismaAt returns a nullPrismaTime that writes NULL when t is
// zero, otherwise the int64 millis.
func nullPrismaAt(t time.Time) nullPrismaTime {
	return nullPrismaTime{Time: t.UTC(), Valid: !t.IsZero()}
}
