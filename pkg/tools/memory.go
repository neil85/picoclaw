package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// memoryCharLimit bounds MEMORY.md because the whole file is injected into
// every system prompt; a small cap forces the model to consolidate.
const memoryCharLimit = 4000

// memoryInjection rejects entries that try to steer the model from the system prompt.
var memoryInjection = regexp.MustCompile(
	`(?i)ignore (all |any )?(previous|prior|above) (instructions|prompts)|disregard (the |your )?(system|previous)|you are now |system prompt:`)

// MemoryTool edits the long-term memory file (memory/MEMORY.md) as discrete
// bullet entries: add, replace and remove, with a size cap and atomic writes.
type MemoryTool struct {
	file string
	mu   sync.Mutex
}

func NewMemoryTool(workspace string) *MemoryTool {
	return &MemoryTool{file: filepath.Join(workspace, "memory", "MEMORY.md")}
}

func (t *MemoryTool) Name() string { return "memory" }

func (t *MemoryTool) Description() string {
	return "Manage long-term memory that is shown to you in every future conversation. " +
		"Save durable facts only: user preferences, environment details, project conventions, lessons learned. " +
		"Do not save task progress, temporary state or secrets. Keep entries short; " +
		fmt.Sprintf("total size is capped at %d characters, so replace or remove stale entries first. ", memoryCharLimit) +
		"Actions: add (text), replace (match -> text), remove (match), list. " +
		"'match' is a unique substring of the existing entry."
}

func (t *MemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"add", "replace", "remove", "list"}},
			"text":   map[string]any{"type": "string", "description": "Entry text for add/replace"},
			"match":  map[string]any{"type": "string", "description": "Unique substring of the entry to replace/remove"},
		},
		"required": []string{"action"},
	}
}

func (t *MemoryTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	action, _ := args["action"].(string)
	text, _ := args["text"].(string)
	match, _ := args["match"].(string)
	text = strings.Join(strings.Fields(text), " ") // one line per entry
	match = strings.TrimSpace(match)

	t.mu.Lock()
	defer t.mu.Unlock()

	var lines []string
	if data, err := os.ReadFile(t.file); err == nil {
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}

	if action == "list" {
		return NewToolResult(renderMemory(lines))
	}
	if action == "add" || action == "replace" {
		if text == "" {
			return ErrorResult("'text' is required for " + action)
		}
		if memoryInjection.MatchString(text) {
			return ErrorResult("Entry rejected: it looks like a prompt-injection instruction, not a fact.")
		}
	}

	switch action {
	case "add":
		for _, l := range lines {
			if strings.EqualFold(strings.TrimPrefix(l, "- "), text) {
				return NewToolResult("Already in memory; nothing changed.")
			}
		}
		lines = append(lines, "- "+text)
	case "replace", "remove":
		idx, errMsg := uniqueMatch(lines, match)
		if errMsg != "" {
			return ErrorResult(errMsg)
		}
		if action == "remove" {
			lines = append(lines[:idx], lines[idx+1:]...)
		} else {
			lines[idx] = "- " + text
		}
	default:
		return ErrorResult("Unknown action. Use add, replace, remove or list.")
	}

	if n := len(strings.Join(lines, "\n")); n > memoryCharLimit {
		return ErrorResult(fmt.Sprintf(
			"Memory would be %d/%d characters. Remove or shorten stale entries first.\n%s", n, memoryCharLimit, renderMemory(lines)))
	}
	if err := os.MkdirAll(filepath.Dir(t.file), 0o755); err != nil {
		return ErrorResult("memory write failed: " + err.Error())
	}
	if err := fileutil.WriteFileAtomic(t.file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return ErrorResult("memory write failed: " + err.Error())
	}
	return NewToolResult("Saved.\n" + renderMemory(lines))
}

func uniqueMatch(lines []string, match string) (int, string) {
	if match == "" {
		return 0, "'match' is required"
	}
	idx, n := -1, 0
	for i, l := range lines {
		if strings.Contains(strings.ToLower(l), strings.ToLower(match)) {
			idx, n = i, n+1
		}
	}
	switch n {
	case 0:
		return 0, fmt.Sprintf("No entry contains %q.", match)
	case 1:
		return idx, ""
	}
	return 0, fmt.Sprintf("%q matches %d entries; use a more specific substring.", match, n)
}

func renderMemory(lines []string) string {
	if len(lines) == 0 {
		return "(memory is empty)"
	}
	return fmt.Sprintf("Memory (%d/%d chars):\n%s", len(strings.Join(lines, "\n")), memoryCharLimit, strings.Join(lines, "\n"))
}
