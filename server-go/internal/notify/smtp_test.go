package notify

// smtp_test.go covers smtpSendMail's cancellation behavior — the whole
// point of replacing the goroutine-wrapped smtp.SendMail: a hung SMTP
// server must not pin the caller (or leak a socket) past ctx.

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// hangServer accepts connections and never writes a byte — the shape of
// a wedged SMTP server that previously leaked one goroutine + socket
// per outbox retry.
func hangServer(t *testing.T) (host, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the conn open, say nothing.
			defer conn.Close()
		}
	}()
	h, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	return h, p
}

func TestSMTPSendMail_CancelTearsDownHungConn(t *testing.T) {
	t.Parallel()
	host, port := hangServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- smtpSendMail(ctx, host, port, nil, "a@b", []string{"c@d"}, []byte("hi"))
	}()
	// Let the dial land, then cancel — the watchdog must close the conn
	// and unblock the greeting read immediately.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("send against a mute server succeeded?")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("smtpSendMail still blocked 3s after cancel — conn not torn down")
	}
}

func TestSMTPSendMail_RejectsCRLFAddresses(t *testing.T) {
	t.Parallel()
	err := smtpSendMail(context.Background(), "localhost", "25", nil,
		"a@b", []string{"c@d\r\nRCPT TO:<evil@e>"}, []byte("hi"))
	if err == nil || !strings.Contains(err.Error(), "CR/LF") {
		t.Fatalf("got %v, want CR/LF rejection (before any dial)", err)
	}
}
