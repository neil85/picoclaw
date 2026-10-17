package a2a

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/inbox"
)

// Regression for the load test: with nothing answering, accepted wake tasks must
// stop at max_in_flight and the rest get 429 (previously ~150k queued, 1.4 GB RSS).
func TestWakeBackpressure(t *testing.T) {
	c, _, _ := newTestChannel(t)
	c.cfg.MaxInFlight = 3
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"parts":[{"text":"hi"}]}}}`))
		req.Header.Set("Authorization", "Bearer s3cret")
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, req)
		return rec.Code
	}
	// bus buffer (64) is larger than the limit, so publishing never blocks here.
	ok, busy := 0, 0
	for i := 0; i < 10; i++ {
		switch send() {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			busy++
		}
	}
	if ok != 3 || busy != 7 {
		t.Fatalf("accepted=%d busy=%d, want 3 and 7", ok, busy)
	}
	// inbox-mode is not a turn and is never throttled by the in-flight limit.
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(
		`{"jsonrpc":"2.0","id":2,"method":"message/send","params":{"message":{"parts":[{"text":"note"}]},"metadata":{"mode":"inbox"}}}`))
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("inbox mode should still work when busy: %d %s", rec.Code, rec.Body.String())
	}
}

// Regression: concurrent Add calls used to overshoot MaxItems (64 files for a cap of 50).
func TestInboxCapUnderConcurrency(t *testing.T) {
	ws := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = inbox.Add(ws, "x", "m") }()
	}
	wg.Wait()
	if n := strings.Count(inbox.Render(ws), "## id="); n > inbox.MaxItems {
		t.Fatalf("inbox holds %d items, cap is %d", n, inbox.MaxItems)
	}
	if got := strings.Count(inbox.Render(ws), "# Inbox ("+"50 pending"); got != 1 {
		t.Errorf("expected exactly %d pending, render header: %.80q", inbox.MaxItems, inbox.Render(ws))
	}
}
