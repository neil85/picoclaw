package a2a

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/inbox"
)

func newTestChannel(t *testing.T) (*Channel, *bus.MessageBus, string) {
	t.Helper()
	ws := t.TempDir()
	cfg := &config.A2ASettings{}
	cfg.Token = *config.NewSecureString("s3cret")
	b := bus.NewMessageBus()
	c, err := New(&config.Channel{}, cfg, b, ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, b, ws
}

func rpc(t *testing.T, c *Channel, token, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestA2A(t *testing.T) {
	if _, err := New(&config.Channel{}, &config.A2ASettings{}, bus.NewMessageBus(), t.TempDir()); err == nil {
		t.Error("empty token must be rejected")
	}
	c, b, ws := newTestChannel(t)

	if code, _ := rpc(t, c, "wrong", `{}`); code != http.StatusUnauthorized {
		t.Errorf("bad token: got %d", code)
	}
	if code, _ := rpc(t, c, "", `{}`); code != http.StatusUnauthorized {
		t.Errorf("no token: got %d", code)
	}

	// Agent card needs no auth.
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"agent.json", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"JSONRPC"`) {
		t.Errorf("card: %d %s", rec.Code, rec.Body.String())
	}

	// Inbox mode: no LLM turn, item lands in the inbox and shows in the prompt section.
	_, res := rpc(t, c, "s3cret", `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{
		"message":{"parts":[{"kind":"text","text":"SBC research done: see result.md"}]},
		"metadata":{"mode":"inbox","from":"zero!!"}}}`)
	task := res["result"].(map[string]any)
	if task["status"].(map[string]any)["state"] != "completed" {
		t.Fatalf("inbox task: %v", res)
	}
	if r := inbox.Render(ws); !strings.Contains(r, "SBC research done") || !strings.Contains(r, "From: zero") {
		t.Errorf("inbox render: %q", r)
	}
	select {
	case <-b.InboundChan():
		t.Error("inbox mode must not publish an inbound turn")
	default:
	}
	_, got := rpc(t, c, "s3cret", `{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"id":"`+task["id"].(string)+`"}}`)
	if got["result"] == nil {
		t.Errorf("tasks/get: %v", got)
	}
	if _, nf := rpc(t, c, "s3cret", `{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{"id":"nope"}}`); nf["error"] == nil {
		t.Error("unknown task should error")
	}

	// Wake mode, blocking: the inbound reaches the bus, the reply completes the task.
	go func() {
		select {
		case in := <-b.InboundChan():
			if !strings.Contains(in.Content, "untrusted") && !strings.Contains(in.Content, "not the user") {
				t.Errorf("peer message must be labelled: %q", in.Content)
			}
			_, _ = c.Send(context.Background(), bus.OutboundMessage{ChatID: in.ChatID, Content: "pong"})
		case <-time.After(3 * time.Second):
		}
	}()
	_, res = rpc(t, c, "s3cret", `{"jsonrpc":"2.0","id":4,"method":"message/send","params":{
		"message":{"parts":[{"kind":"text","text":"ping"}]},"configuration":{"blocking":true}}}`)
	task = res["result"].(map[string]any)
	if task["status"].(map[string]any)["state"] != "completed" || !strings.Contains(toJSON(task), "pong") {
		t.Errorf("wake task: %v", res)
	}

	// Validation.
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":5,"method":"message/send","params":{"message":{"parts":[]}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"message/send","params":{"message":{"contextId":"../x","parts":[{"text":"hi"}]}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"nope"}`,
	} {
		if _, r := rpc(t, c, "s3cret", body); r["error"] == nil {
			t.Errorf("expected error for %s", body)
		}
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestInbox(t *testing.T) {
	ws := t.TempDir()
	id, err := inbox.Add(ws, "zero", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Read(ws, "../../etc/passwd"); err == nil {
		t.Error("path traversal must be rejected")
	}
	if _, err := inbox.Ack(ws, "../x"); err == nil {
		t.Error("bad ack id must be rejected")
	}
	if n, err := inbox.Ack(ws, id); err != nil || n != 1 {
		t.Fatalf("ack: %d %v", n, err)
	}
	if inbox.Render(ws) != "" {
		t.Error("acked item must leave the prompt")
	}
	for i := 0; i < inbox.MaxItems; i++ {
		if _, err := inbox.Add(ws, "z", "m"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := inbox.Add(ws, "z", "overflow"); err == nil {
		t.Error("inbox should cap at MaxItems")
	}
	if n := len(inbox.Render(ws)); n > 3500 {
		t.Errorf("prompt section too large: %d", n)
	}
}
