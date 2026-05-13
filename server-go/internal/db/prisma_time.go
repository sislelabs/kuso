package db

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// prismaTime is a thin time.Time wrapper used for every timestamp
// column. lib/pq scans Postgres TIMESTAMPTZ into time.Time directly;
// the wrapper exists so call sites get a single type that knows how
// to write NULL for the zero value.
//
// Name is historical (Prisma-era SQLite int64 millis); kept so call
// sites compile without changes.
type prismaTime struct {
	time.Time
}

// Scan implements sql.Scanner. lib/pq always hands us time.Time for
// TIMESTAMPTZ columns; everything else is a driver bug we want to
// surface, not paper over.
func (p *prismaTime) Scan(value any) error {
	if value == nil {
		p.Time = time.Time{}
		return nil
	}
	t, ok := value.(time.Time)
	if !ok {
		return fmt.Errorf("prismaTime: unsupported scan type %T", value)
	}
	p.Time = t.UTC()
	return nil
}

// Value implements driver.Valuer. Returns nil for the zero time so
// the column is written as NULL.
func (p prismaTime) Value() (driver.Value, error) {
	if p.Time.IsZero() {
		return nil, nil
	}
	return p.Time.UTC(), nil
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
