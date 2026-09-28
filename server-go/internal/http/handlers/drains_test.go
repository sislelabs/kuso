package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/drains"
	"kuso/server/internal/http/handlers"
)

type memDrainStore struct {
	mu sync.Mutex
	m  map[string]drains.Drain
	n  int
}

func (s *memDrainStore) List(context.Context) ([]drains.Drain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []drains.Drain{}
	for _, d := range s.m {
		out = append(out, d)
	}
	return out, nil
}

func (s *memDrainStore) Get(_ context.Context, id string) (*drains.Drain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.m[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", drains.ErrNotFound, id)
	}
	return &d, nil
}

func (s *memDrainStore) Put(_ context.Context, d *drains.Drain, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]drains.Drain{}
	}
	if d.ID == "" {
		s.n++
		d.ID = fmt.Sprintf("d%d", s.n)
	}
	s.m[d.ID] = *d
	return nil
}

func (s *memDrainStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; !ok {
		return drains.ErrNotFound
	}
	delete(s.m, id)
	return nil
}

type fakeDrainTester struct {
	status int
	err    error
	got    drains.Drain
}

func (f *fakeDrainTester) Test(_ context.Context, d drains.Drain) (int, error) {
	f.got = d
	return f.status, f.err
}

func drainRouter(h *handlers.DrainsHandler, perms ...auth.Permission) http.Handler {
	ps := make([]string, len(perms))
	for i, p := range perms {
		ps[i] = string(p)
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := &auth.Claims{UserID: "u1", Username: "admin", Permissions: ps}
			next.ServeHTTP(w, req.WithContext(auth.WithClaimsForTest(req.Context(), c)))
		})
	})
	h.Mount(r)
	return r
}

func doDrain(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func newDrainsHandler() (*handlers.DrainsHandler, *memDrainStore, *fakeDrainTester) {
	st := &memDrainStore{}
	tt := &fakeDrainTester{status: 204}
	return &handlers.DrainsHandler{Store: st, Tester: tt, Logger: slog.Default()}, st, tt
}

const drainMask = "••••••••"

func TestDrainsCreateMasksSecretsOnEveryRead(t *testing.T) {
	h, st, _ := newDrainsHandler()
	r := drainRouter(h, auth.PermSettingsAdmin)
	rr := doDrain(t, r, "POST", "/api/drains",
		`{"type":"loki","url":"https://123:glc_tok@logs.example.com","headers":{"X-Scope-OrgID":"tenant"},"secret":"hmac-key"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Code, rr.Body.String())
	}
	for _, body := range []string{
		rr.Body.String(),
		doDrain(t, r, "GET", "/api/drains", "").Body.String(),
		doDrain(t, r, "GET", "/api/drains/d1", "").Body.String(),
	} {
		for _, leak := range []string{"glc_tok", "hmac-key", "tenant", "Basic "} {
			if strings.Contains(body, leak) {
				t.Fatalf("response leaks %q: %s", leak, body)
			}
		}
		if !strings.Contains(body, drainMask) || !strings.Contains(body, "X-Scope-OrgID") {
			t.Fatalf("expected masked values with header names kept: %s", body)
		}
	}
	stored := st.m["d1"]
	if stored.Secret != "hmac-key" || stored.Headers["X-Scope-OrgID"] != "tenant" || !stored.Enabled {
		t.Fatalf("store must hold the real values, got %+v", stored)
	}
}

func TestDrainsUpdateKeepsMaskedCredentials(t *testing.T) {
	h, st, _ := newDrainsHandler()
	r := drainRouter(h, auth.PermSettingsAdmin)
	doDrain(t, r, "POST", "/api/drains", `{"type":"http","url":"https://hook.example.com","headers":{"Authorization":"Bearer real"},"secret":"s1"}`)
	rr := doDrain(t, r, "PUT", "/api/drains/d1",
		`{"name":"renamed","type":"http","url":"https://hook.example.com","headers":{"Authorization":"`+drainMask+`","X-New":"v"},"secret":"`+drainMask+`","enabled":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rr.Code, rr.Body.String())
	}
	d := st.m["d1"]
	if d.Headers["Authorization"] != "Bearer real" || d.Secret != "s1" || d.Headers["X-New"] != "v" || d.Name != "renamed" || d.Enabled {
		t.Fatalf("masked round-trip clobbered state: %+v", d)
	}
}

func TestDrainsCreateRejectsInvalid(t *testing.T) {
	h, _, _ := newDrainsHandler()
	r := drainRouter(h, auth.PermSettingsAdmin)
	for _, body := range []string{
		`{"type":"http","url":"http://169.254.169.254/latest/meta-data"}`,
		`{"type":"syslog","url":"https://x.example.com"}`,
		`{"type":"http","url":"https://x.example.com","secret":"` + drainMask + `"}`,
	} {
		if rr := doDrain(t, r, "POST", "/api/drains", body); rr.Code != http.StatusBadRequest {
			t.Fatalf("%s → %d, want 400 (%s)", body, rr.Code, rr.Body.String())
		}
	}
}

func TestDrainsCreateRejectsUnknownProject(t *testing.T) {
	h, _, _ := newDrainsHandler()
	h.ProjectExists = func(_ context.Context, p string) (bool, error) { return p == "shop", nil }
	r := drainRouter(h, auth.PermSettingsAdmin)
	if rr := doDrain(t, r, "POST", "/api/drains", `{"type":"http","url":"https://x.example.com","project":"nope"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown project → %d", rr.Code)
	}
	if rr := doDrain(t, r, "POST", "/api/drains", `{"type":"http","url":"https://x.example.com","project":"shop"}`); rr.Code != http.StatusCreated {
		t.Fatalf("known project → %d %s", rr.Code, rr.Body.String())
	}
}

func TestDrainsTestReportsUpstream(t *testing.T) {
	h, _, tt := newDrainsHandler()
	r := drainRouter(h, auth.PermSettingsAdmin)
	doDrain(t, r, "POST", "/api/drains", `{"type":"otlp","url":"https://otlp.example.com","secret":"s"}`)

	rr := doDrain(t, r, "POST", "/api/drains/d1/test", "")
	var ok struct {
		OK     bool `json:"ok"`
		Status int  `json:"status"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &ok)
	if rr.Code != http.StatusOK || !ok.OK || ok.Status != 204 {
		t.Fatalf("test ok path = %d %s", rr.Code, rr.Body.String())
	}
	if tt.got.Secret != "s" {
		t.Fatal("tester must get the unmasked drain")
	}

	tt.status, tt.err = 401, errors.New("upstream 401: invalid token")
	rr = doDrain(t, r, "POST", "/api/drains/d1/test", "")
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "invalid token") || !strings.Contains(rr.Body.String(), `"upstreamStatus":401`) {
		t.Fatalf("test failure path = %d %s", rr.Code, rr.Body.String())
	}
}

func TestDrainsDelete(t *testing.T) {
	h, st, _ := newDrainsHandler()
	r := drainRouter(h, auth.PermSettingsAdmin)
	doDrain(t, r, "POST", "/api/drains", `{"type":"http","url":"https://x.example.com"}`)
	if rr := doDrain(t, r, "DELETE", "/api/drains/d1", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rr.Code)
	}
	if len(st.m) != 0 {
		t.Fatal("not deleted")
	}
	if rr := doDrain(t, r, "DELETE", "/api/drains/d1", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", rr.Code)
	}
}

func TestDrainsAdminOnly(t *testing.T) {
	h, _, _ := newDrainsHandler()
	r := drainRouter(h, auth.PermProjectRead)
	if rr := doDrain(t, r, "GET", "/api/drains", ""); rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin list = %d, want 403", rr.Code)
	}
}
