package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hdm/toneloc/internal/engine"
)

func TestSaveLoadHandlers(t *testing.T) {
	mask, _ := engine.ParseMask("192.0.2.X")
	job := engine.Job{DataFile: filepath.Join(t.TempDir(), "TEST.DAT"), Mask: mask, Ports: []uint16{80},
		WaitDelay: 2 * time.Millisecond, MaxRings: 2, Seed: 3, Backend: "sim", Limit: 20}
	eng, err := engine.New(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	eng.Run(context.Background())

	sessions.Store("sess1", eng)
	defer sessions.Delete("sess1")

	// Save: download the current state.
	rec := httptest.NewRecorder()
	handleSave(rec, httptest.NewRequest("GET", "/save?s=sess1", nil))
	if rec.Code != 200 {
		t.Fatalf("save status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ToneLoc/Go data file") {
		t.Fatalf("save body not a data file: %q", body[:40])
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "TEST.DAT") {
		t.Errorf("missing/incorrect filename header: %q", cd)
	}

	// Load: merge the same state into a fresh session (everything is a dup -> 0 new
	// for the same engine, so use a second engine).
	eng2, _ := engine.New(context.Background(), job)
	sessions.Store("sess2", eng2)
	defer sessions.Delete("sess2")
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/load?s=sess2", strings.NewReader(body))
	handleLoad(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("load status %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), `"merged"`) {
		t.Fatalf("load response: %s", rec2.Body.String())
	}

	// Unknown session -> 404.
	rec3 := httptest.NewRecorder()
	handleSave(rec3, httptest.NewRequest("GET", "/save?s=nope", nil))
	if rec3.Code != http.StatusNotFound {
		t.Errorf("unknown session save = %d, want 404", rec3.Code)
	}
}
