package db

import (
	"errors"

	"github.com/lib/pq"
)

// IsUniqueViolation reports whether err (or anything it wraps) is a
// Postgres unique-constraint violation, SQLSTATE 23505. Handlers use it
// to answer 409 for a duplicate name instead of a bare 500.
func IsUniqueViolation(err error) bool {
	var pqe *pq.Error
	return errors.As(err, &pqe) && pqe.Code == "23505"
}
