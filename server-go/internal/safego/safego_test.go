package safego

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestGoRecoversAndLogs(t *testing.T) {
	var buf syncBuf
	Go(slog.New(slog.NewTextHandler(&buf, nil)), "boom-loop", func() { panic("kaboom") })
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "kaboom") {
		if time.Now().After(deadline) {
			t.Fatal("panic was not logged")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), "boom-loop") {
		t.Errorf("log lacks the goroutine name: %s", buf.String())
	}
}
