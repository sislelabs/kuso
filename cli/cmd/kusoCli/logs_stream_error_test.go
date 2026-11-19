package kusoCli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"kuso/pkg/kusoApi"
)

// The server reports a missing env as {"type":"error","message":…}
// then a Normal close; that used to print a blank error and exit 0.
func TestStreamLogs_ErrorFrameFailsCommand(t *testing.T) {
	up := websocket.Upgrader{Subprotocols: []string{"kuso.bearer"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]string{"type": "error", "message": "env staging not found"})
		_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_, _, _ = c.ReadMessage()
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	err := streamLogs("p", "s", "staging", 10)
	if err == nil || !strings.Contains(err.Error(), "env staging not found") {
		t.Fatalf("want error carrying the server message, got %v", err)
	}
}
