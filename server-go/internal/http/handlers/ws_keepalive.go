package handlers

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

// Dead-peer detection for long-lived WS sessions. A client that vanishes
// without FIN/RST (laptop lid, Wi-Fi drop, NAT expiry) is otherwise only
// noticed when TCP retransmission gives up (~15 min), holding its per-user
// slot and the kubelet stream the whole time. Vars so tests can shrink them.
var (
	wsPingInterval = 20 * time.Second
	wsPongWait     = 60 * time.Second
)

// Inbound frame caps for the interactive sessions. Port-forward clients (the
// CLI) send at most 32 KiB per frame; the terminal gets more headroom because
// a browser paste arrives as one frame.
const (
	wsPortForwardMaxInbound = 64 << 10
	wsTerminalMaxInbound    = 256 << 10
)

// wsKeepalive pings the peer and extends the read deadline whenever a pong
// arrives. Durations are captured at start so a session never reads the
// package vars again.
type wsKeepalive struct {
	conn     *websocket.Conn
	pongWait time.Duration
}

// startWSKeepalive pings the peer every wsPingInterval until ctx is done.
// Callers must read via the returned keepalive's read so a stalled reader
// on our side doesn't trip the deadline on its own.
func startWSKeepalive(ctx context.Context, conn *websocket.Conn) *wsKeepalive {
	k := &wsKeepalive{conn: conn, pongWait: wsPongWait}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(k.pongWait))
	})
	t := time.NewTicker(wsPingInterval)
	go func() {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout)); err != nil {
					return
				}
			}
		}
	}()
	return k
}

// read reads one message with a fresh pong-wait deadline, so only a peer
// that stops answering pings (not a slow consumer on our side) times out.
func (k *wsKeepalive) read() (int, []byte, error) {
	if err := k.conn.SetReadDeadline(time.Now().Add(k.pongWait)); err != nil {
		return 0, nil, err
	}
	return k.conn.ReadMessage()
}

func isWSTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
