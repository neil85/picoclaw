package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newApprovalTestHook(t *testing.T, cfg channelApprovalConfig) (*AgentLoop, *channelApprovalHook) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	al := &AgentLoop{bus: bus.NewMessageBus(), hooks: NewHookManager(nil)}
	h, err := al.newChannelApprovalHook(config.BuiltinHookConfig{Enabled: true, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	return al, h
}

func approvalReq(tool, command string) *ToolApprovalRequest {
	return &ToolApprovalRequest{
		Meta: HookMeta{SessionKey: "s1"},
		Context: &TurnContext{Inbound: &bus.InboundContext{
			Channel: "telegram", ChatID: "c1", SenderID: "u1",
		}},
		Tool:      tool,
		Arguments: map[string]any{"command": command},
	}
}

func decide(t *testing.T, h *channelApprovalHook, req *ToolApprovalRequest) <-chan ApprovalDecision {
	t.Helper()
	out := make(chan ApprovalDecision, 1)
	go func() {
		d, _ := h.ApproveTool(context.Background(), req)
		out <- d
	}()
	return out
}

func waitPending(t *testing.T, al *AgentLoop) {
	t.Helper()
	for i := 0; i < 200; i++ {
		al.approvals.mu.Lock()
		n := len(al.approvals.pending["s1"])
		al.approvals.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("approval never became pending")
}

func TestChannelApproval(t *testing.T) {
	al, h := newApprovalTestHook(t, channelApprovalConfig{AllowPatterns: []string{`^ls\b`}, TimeoutSeconds: 1})

	// untracked tool and allow-listed command pass without asking
	if d, _ := h.ApproveTool(context.Background(), approvalReq("read_file", "x")); !d.Approved {
		t.Fatal("untracked tool should be approved")
	}
	if d, _ := h.ApproveTool(context.Background(), approvalReq("exec", "ls -la")); !d.Approved {
		t.Fatal("allow-listed command should be approved")
	}

	// approve
	out := decide(t, h, approvalReq("exec", "rm -rf /tmp/x"))
	waitPending(t, al)
	if r := al.approvals.resolve("s1", "intruder", true, false); r == "Approved." {
		t.Fatal("other sender must not be able to approve")
	}
	al.approvals.resolve("s1", "u1", true, false)
	if d := <-out; !d.Approved {
		t.Fatalf("expected approved, got %+v", d)
	}

	// deny
	out = decide(t, h, approvalReq("exec", "rm -rf /tmp/x"))
	waitPending(t, al)
	al.approvals.resolve("s1", "u1", false, false)
	if d := <-out; d.Approved {
		t.Fatal("expected denied")
	}

	// approve always: same command is not asked again
	out = decide(t, h, approvalReq("exec", "make deploy"))
	waitPending(t, al)
	al.approvals.resolve("s1", "u1", true, true)
	<-out
	if d, _ := h.ApproveTool(context.Background(), approvalReq("exec", "make deploy")); !d.Approved {
		t.Fatal("always-approved command should not prompt again")
	}

	// timeout denies
	if d, _ := h.ApproveTool(context.Background(), approvalReq("exec", "reboot")); d.Approved {
		t.Fatal("timeout must deny")
	}

	// nothing pending
	if r := al.approvals.resolve("s1", "u1", true, false); r != "No pending approval." {
		t.Fatalf("unexpected reply %q", r)
	}
}
