package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// channelApprovalHookName is the config key under hooks.builtins that enables
// interactive tool approval over the chat channel the turn came from.
const channelApprovalHookName = "channel_approval"

const (
	defaultChannelApprovalTimeout = 120 * time.Second
	approvalHookTimeoutSlack      = 5 * time.Second
	approvalPromptPreviewLen      = 500
)

// channelApprovalConfig is the hooks.builtins.channel_approval.config payload.
type channelApprovalConfig struct {
	// Tools that require approval. Defaults to ["exec"].
	Tools []string `json:"tools,omitempty"`
	// AllowPatterns are regexes matched against the exec "command" argument;
	// a match is approved without asking.
	AllowPatterns []string `json:"allow_patterns,omitempty"`
	// TimeoutSeconds is how long to wait for a reply before denying. Default 120.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

type pendingApproval struct {
	senderID string
	key      string
	done     chan bool // buffered(1); true = approved
}

// approvalBroker tracks approval requests waiting for a /approve or /deny
// reply, per session. The zero value is ready to use.
type approvalBroker struct {
	mu      sync.Mutex
	pending map[string][]*pendingApproval
	always  map[string]map[string]struct{}
}

func (b *approvalBroker) isAlways(sessionKey, key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.always[sessionKey][key]
	return ok
}

func (b *approvalBroker) add(sessionKey string, p *pendingApproval) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		b.pending = make(map[string][]*pendingApproval)
	}
	b.pending[sessionKey] = append(b.pending[sessionKey], p)
}

func (b *approvalBroker) remove(sessionKey string, p *pendingApproval) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.pending[sessionKey]
	for i, q := range list {
		if q == p {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(b.pending, sessionKey)
	} else {
		b.pending[sessionKey] = list
	}
}

// resolve answers the oldest pending approval of the session and returns the
// reply text for the user. Only the user whose message triggered the request
// may answer it (when both sender IDs are known).
func (b *approvalBroker) resolve(sessionKey, senderID string, approved, always bool) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	list := b.pending[sessionKey]
	if len(list) == 0 {
		return "No pending approval."
	}
	p := list[0]
	if p.senderID != "" && senderID != "" && p.senderID != senderID {
		return "Only the user who made the request can answer this approval."
	}
	if len(list) == 1 {
		delete(b.pending, sessionKey)
	} else {
		b.pending[sessionKey] = list[1:]
	}

	if approved && always {
		if b.always == nil {
			b.always = make(map[string]map[string]struct{})
		}
		if b.always[sessionKey] == nil {
			b.always[sessionKey] = make(map[string]struct{})
		}
		b.always[sessionKey][p.key] = struct{}{}
	}
	p.done <- approved

	switch {
	case !approved:
		return "Denied."
	case always:
		return "Approved (always, for this session)."
	default:
		return "Approved."
	}
}

type channelApprovalHook struct {
	al      *AgentLoop
	tools   map[string]struct{}
	allow   []*regexp.Regexp
	timeout time.Duration
}

func (al *AgentLoop) newChannelApprovalHook(spec config.BuiltinHookConfig) (*channelApprovalHook, error) {
	var cfg channelApprovalConfig
	if len(spec.Config) > 0 {
		if err := json.Unmarshal(spec.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	h := &channelApprovalHook{al: al, tools: map[string]struct{}{}, timeout: defaultChannelApprovalTimeout}
	if cfg.TimeoutSeconds > 0 {
		h.timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	if len(cfg.Tools) == 0 {
		cfg.Tools = []string{"exec"}
	}
	for _, t := range cfg.Tools {
		h.tools[t] = struct{}{}
	}
	for _, p := range cfg.AllowPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid allow_pattern %q: %w", p, err)
		}
		h.allow = append(h.allow, re)
	}

	// The HookManager caps every approval hook at its own timeout; make sure
	// it outlasts the time we are willing to wait for a human.
	al.hooks.ConfigureTimeouts(0, 0, h.timeout+approvalHookTimeoutSlack)
	return h, nil
}

func approvalSummary(req *ToolApprovalRequest) (key, preview string) {
	if cmd, ok := req.Arguments["command"].(string); ok {
		return req.Tool + ":" + cmd, cmd
	}
	raw, _ := json.Marshal(req.Arguments)
	return req.Tool + ":" + string(raw), string(raw)
}

func (h *channelApprovalHook) ApproveTool(ctx context.Context, req *ToolApprovalRequest) (ApprovalDecision, error) {
	if _, ok := h.tools[req.Tool]; !ok {
		return ApprovalDecision{Approved: true}, nil
	}

	key, preview := approvalSummary(req)
	if cmd, ok := req.Arguments["command"].(string); ok {
		// Hardline commands are never offered for approval, allow-listed or remembered.
		if reason := tools.HardlineReason(cmd); reason != "" {
			return ApprovalDecision{Approved: false, Reason: "hardline rule: " + reason}, nil
		}
		for _, re := range h.allow {
			if re.MatchString(cmd) {
				return ApprovalDecision{Approved: true}, nil
			}
		}
	}

	sessionKey := req.Meta.SessionKey
	if h.al.approvals.isAlways(sessionKey, key) {
		return ApprovalDecision{Approved: true}, nil
	}

	if req.Context == nil || req.Context.Inbound == nil ||
		req.Context.Inbound.Channel == "" || req.Context.Inbound.ChatID == "" {
		return ApprovalDecision{Approved: false, Reason: "no chat available to ask for approval"}, nil
	}
	in := req.Context.Inbound

	p := &pendingApproval{senderID: in.SenderID, key: key, done: make(chan bool, 1)}
	h.al.approvals.add(sessionKey, p)
	defer h.al.approvals.remove(sessionKey, p)

	prompt := fmt.Sprintf(
		"⚠️ Approval required\nTool: %s\n%s\n\nReply /approve, /approve always or /deny (expires in %ds).",
		req.Tool, utils.Truncate(preview, approvalPromptPreviewLen), int(h.timeout.Seconds()),
	)
	if err := h.al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Context:    bus.NewOutboundContext(in.Channel, in.ChatID, ""),
		SessionKey: sessionKey,
		Content:    prompt,
	}); err != nil {
		logger.WarnCF("hooks", "Failed to publish approval prompt", map[string]any{"error": err.Error()})
		return ApprovalDecision{Approved: false, Reason: "could not ask for approval"}, nil
	}

	timer := time.NewTimer(h.timeout)
	defer timer.Stop()
	select {
	case approved := <-p.done:
		if approved {
			return ApprovalDecision{Approved: true}, nil
		}
		return ApprovalDecision{Approved: false, Reason: "denied by user"}, nil
	case <-timer.C:
		return ApprovalDecision{Approved: false, Reason: "approval request timed out"}, nil
	case <-ctx.Done():
		return ApprovalDecision{Approved: false, Reason: "approval request canceled"}, nil
	}
}

// tryHandleApprovalCommand answers /approve and /deny while a turn is blocked
// on an approval prompt (the session is busy, so the message would otherwise
// be queued as steering input).
func (al *AgentLoop) tryHandleApprovalCommand(ctx context.Context, msg bus.InboundMessage, sessionKey string) bool {
	name, ok := commands.CommandName(msg.Content)
	if !ok || (name != "approve" && name != "deny") {
		return false
	}
	fields := strings.Fields(msg.Content)
	always := len(fields) > 1 && strings.EqualFold(fields[1], "always")
	reply := al.approvals.resolve(sessionKey, msg.SenderID, name == "approve", always)
	al.PublishResponseIfNeeded(ctx, msg.Channel, msg.ChatID, sessionKey, reply)
	return true
}
