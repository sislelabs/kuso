package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// TicketConsumer records a WebSocket ticket's jti and reports whether it
// was the first use. It must be shared by every replica (Postgres), or a
// captured ticket opens one socket per replica.
type TicketConsumer func(ctx context.Context, jti string, expiresAt time.Time) (bool, error)

// SetTicketConsumer installs the shared single-use store for WebSocket
// tickets. Without one (tests, a kube-less dev server) tickets are
// single-use within this process only. Call once at startup.
func (i *Issuer) SetTicketConsumer(fn TicketConsumer) {
	i.tickets = fn
}

// ConsumeWSTicket marks a ticket used and reports whether this was its
// first use. A store error fails closed: the client mints a new ticket.
func (i *Issuer) ConsumeWSTicket(ctx context.Context, jti string, expiresAt time.Time) bool {
	if jti == "" {
		return false
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(WSTicketTTL)
	}
	if i.tickets == nil {
		return localTickets.consume(jti, expiresAt)
	}
	ok, err := i.tickets(ctx, jti, expiresAt)
	if err != nil {
		slog.Default().Warn("ws ticket: consume failed; refusing the ticket", "err", err)
		return false
	}
	return ok
}

type localTicketStore struct {
	mu   sync.Mutex
	used map[string]time.Time
}

var localTickets = &localTicketStore{used: map[string]time.Time{}}

func (s *localTicketStore) consume(id string, exp time.Time) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.used {
		if now.After(e) {
			delete(s.used, k)
		}
	}
	if _, seen := s.used[id]; seen {
		return false
	}
	s.used[id] = exp
	return true
}
