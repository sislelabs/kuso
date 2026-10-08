package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// pipeStream is an in-memory stand-in for the kube port-forward data stream.
type pipeStream struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeStream) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeStream) Write(b []byte) (int, error) { return len(b), nil }
func (p pipeStream) Close() error                { _ = p.w.Close(); return p.r.Close() }

func shrinkKeepalive(t *testing.T) {
	t.Helper()
	pi, pw := wsPingInterval, wsPongWait
	wsPingInterval, wsPongWait = 50*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { wsPingInterval, wsPongWait = pi, pw })
}

// runBridge serves one WS that bridges to an idle data stream and reports
// when bridge returns. The client side is returned for the test to drive.
func runBridge(t *testing.T) (*websocket.Conn, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		pr, pw := io.Pipe()
		ds := pipeStream{r: pr, w: pw}
		defer ds.Close()
		bridge(context.Background(), conn, ds, slog.Default())
		once.Do(func() { close(done) })
	}))
	t.Cleanup(srv.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		c.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("bridge did not return after the client closed")
		}
	})
	return c, done
}

func TestBridge_EndsWhenPeerStopsAnsweringPings(t *testing.T) {
	shrinkKeepalive(t)
	c, done := runBridge(t)
	// Swallow pings without ponging: a peer that's gone but whose TCP
	// connection hasn't been torn down.
	c.SetPingHandler(func(string) error { return nil })
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge kept a silent peer's session open")
	}
}

func TestBridge_StaysOpenForResponsivePeer(t *testing.T) {
	shrinkKeepalive(t)
	c, done := runBridge(t)
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
		t.Fatal("bridge closed a session whose peer answers pings")
	case <-time.After(3 * wsPongWait):
	}
}

func TestBridge_RejectsOversizedFrame(t *testing.T) {
	c, done := runBridge(t)
	if err := c.WriteMessage(websocket.BinaryMessage, make([]byte, wsPortForwardMaxInbound+1)); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge accepted a frame over the inbound limit")
	}
}
