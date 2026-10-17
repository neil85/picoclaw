package agent

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/inbox"
)

func TestInboxInSystemPrompt(t *testing.T) {
	ws := setupWorkspace(t, nil)
	defer os.RemoveAll(ws)
	cb := NewContextBuilder(ws)

	if strings.Contains(cb.BuildSystemPromptWithCache(), "# Inbox") {
		t.Fatal("empty inbox must add nothing to the prompt")
	}

	id, err := inbox.Add(ws, "zero", "result ready in result.md")
	if err != nil {
		t.Fatal(err)
	}
	if sp := cb.BuildSystemPromptWithCache(); !strings.Contains(sp, "result ready in result.md") {
		t.Fatal("new inbox item should invalidate the cached prompt and appear")
	}

	if _, err := inbox.Ack(ws, id); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second) // mtime-resolution safety, as in context_cache_test.go
	os.Chtimes(inbox.Dir(ws), future, future)
	if sp := cb.BuildSystemPromptWithCache(); strings.Contains(sp, "result ready in result.md") {
		t.Fatal("acknowledged item should leave the prompt")
	}
}
