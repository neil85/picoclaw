package a2a

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/inbox"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// The a2a client tool talking to a real a2a server channel (picoclaw <-> picoclaw).
func TestClientToServer(t *testing.T) {
	c, b, ws := newTestChannel(t)
	srv := httptest.NewServer(c)
	defer srv.Close()

	tool, err := tools.NewA2ATool("jarvis", map[string]tools.A2APeer{
		"zero": {URL: srv.URL + path, Token: "s3cret"},
		"bad":  {URL: srv.URL + path, Token: "wrong"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := func(ctx context.Context, a map[string]any) *tools.ToolResult { return tool.Execute(ctx, a) }

	// inbox mode lands in the server's inbox, labelled with the client's name.
	r := run(ctx, map[string]any{"action": "send", "peer": "zero", "mode": "inbox", "message": "job done"})
	if r.IsError || !strings.Contains(r.ForLLM, "state=completed") {
		t.Fatalf("inbox send: %+v", r)
	}
	if got := inbox.Render(ws); !strings.Contains(got, "job done") || !strings.Contains(got, "From: jarvis") {
		t.Errorf("inbox: %q", got)
	}

	// wake mode: the server's agent replies, the client gets it back marked untrusted.
	go func() {
		select {
		case in := <-b.InboundChan():
			_, _ = c.Send(ctx, bus.OutboundMessage{ChatID: in.ChatID, Content: "42"})
		case <-time.After(3 * time.Second):
		}
	}()
	r = run(ctx, map[string]any{"action": "send", "peer": "zero", "message": "meaning of life?"})
	if r.IsError || !strings.Contains(r.ForLLM, "42") || !strings.Contains(r.ForLLM, "untrusted") {
		t.Fatalf("wake send: %+v", r)
	}
	id := strings.Fields(strings.TrimPrefix(r.ForLLM, "task_id="))[0]
	if g := run(ctx, map[string]any{"action": "get", "peer": "zero", "task_id": id}); g.IsError || !strings.Contains(g.ForLLM, "42") {
		t.Errorf("get: %+v", g)
	}

	// failures are reported, not panicked on.
	if r := run(ctx, map[string]any{"action": "send", "peer": "bad", "mode": "inbox", "message": "x"}); !r.IsError {
		t.Error("wrong token should fail")
	}
	if r := run(ctx, map[string]any{"action": "send", "peer": "nobody", "message": "x"}); !r.IsError {
		t.Error("unknown peer should fail")
	}

	// loop guard: while answering a peer (channel "a2a"), wake-mode sends are refused.
	peerCtx := tools.WithToolContext(ctx, "a2a", "a2a-xyz")
	if r := run(peerCtx, map[string]any{"action": "send", "peer": "zero", "message": "ping back"}); !r.IsError {
		t.Error("wake send from an a2a turn must be refused")
	}
	if r := run(peerCtx, map[string]any{"action": "send", "peer": "zero", "mode": "inbox", "message": "ack"}); r.IsError {
		t.Errorf("inbox send from an a2a turn is allowed: %s", r.ForLLM)
	}

	if _, err := tools.NewA2ATool("x", map[string]tools.A2APeer{"p": {URL: "file:///etc/passwd"}}); err == nil {
		t.Error("non-http peer url must be rejected")
	}
}
