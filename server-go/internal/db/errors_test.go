package db

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
)

func TestIsUniqueViolation(t *testing.T) {
	wrapped := fmt.Errorf("db: create group: %w", &pq.Error{Code: "23505"})
	if !IsUniqueViolation(wrapped) {
		t.Error("wrapped 23505 not detected")
	}
	if IsUniqueViolation(fmt.Errorf("x: %w", &pq.Error{Code: "23503"})) {
		t.Error("foreign-key violation reported as unique")
	}
	if IsUniqueViolation(errors.New("duplicate key value")) {
		t.Error("plain error reported as unique")
	}
}
