package commands

import (
	"context"
	"strings"
)

// approveCommand and denyCommand answer a pending tool-approval prompt. While
// a turn is blocked on a prompt the agent loop intercepts these messages
// itself; the definitions here cover the idle case and /help listing.
func approveCommand() Definition {
	return Definition{
		Name:        "approve",
		Description: "Approve a pending tool request (add 'always' to remember it for this session)",
		Usage:       "/approve [always]",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.ResolveApproval == nil {
				return req.Reply(unavailableMsg)
			}
			always := strings.EqualFold(nthToken(req.Text, 1), "always")
			return req.Reply(rt.ResolveApproval(req.SenderID, true, always))
		},
	}
}

func denyCommand() Definition {
	return Definition{
		Name:        "deny",
		Description: "Deny a pending tool request",
		Usage:       "/deny",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.ResolveApproval == nil {
				return req.Reply(unavailableMsg)
			}
			return req.Reply(rt.ResolveApproval(req.SenderID, false, false))
		},
	}
}
