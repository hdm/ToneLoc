package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/hdm/toneloc/internal/engine"
)

func TestWebSocketSession(t *testing.T) {
	mask, err := engine.ParseMask("192.0.2.X")
	if err != nil {
		t.Fatal(err)
	}
	job := engine.Job{
		Mask: mask, Ports: []uint16{80}, WaitDelay: 200 * time.Millisecond,
		MaxRings: 4, Seed: 7, Backend: "sim",
	}

	srv := httptest.NewServer(websocket.Handler(func(c *websocket.Conn) {
		serveSession(c, job)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, err := websocket.Dial(wsURL, "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// We should receive ANSI render output within a moment.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got strings.Builder
	buf := make([]byte, 8192)
	for i := 0; i < 5; i++ {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		got.Write(buf[:n])
		if strings.Contains(got.String(), "Activity Log") {
			break
		}
	}
	if !strings.Contains(got.String(), "\x1b[") {
		t.Fatalf("expected ANSI escapes from server, got %q", truncate(got.String()))
	}
	if !strings.Contains(got.String(), "Activity Log") {
		t.Fatalf("expected the Activity Log window in output, got %q", truncate(got.String()))
	}

	// Send a quit key (ESC); the session should accept input without error.
	if _, err := conn.Write([]byte{27}); err != nil {
		t.Fatalf("sending key: %v", err)
	}
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
