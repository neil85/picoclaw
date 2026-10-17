package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	maxTodos       = 30
	keepClosed     = 5  // shared list: closed items kept for context, older ones are dropped
	renderMaxItems = 15 // open shared items shown in the system prompt
)

var todoStatuses = map[string]string{"pending": " ", "in_progress": "~", "waiting": "?", "completed": "x", "cancelled": "-"}

type todoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

func (i todoItem) open() bool { return i.Status != "completed" && i.Status != "cancelled" }

// TodoTool keeps task lists persisted under workspace/todos so they survive
// restarts and context compaction. Two scopes:
//   - session (default): the whole list is replaced on each call.
//   - shared: one list across all sessions and channels; items are upserted by id
//     and open items are shown in the system prompt (see RenderSharedTodos).
type TodoTool struct {
	dir string
	mu  sync.Mutex
}

func NewTodoTool(workspace string) *TodoTool {
	return &TodoTool{dir: todoDir(workspace)}
}

func todoDir(workspace string) string { return filepath.Join(workspace, "todos") }

// SharedTodoPath is the file backing the shared list (tracked for prompt-cache invalidation).
func SharedTodoPath(workspace string) string { return filepath.Join(todoDir(workspace), "shared.json") }

func (t *TodoTool) Name() string { return "todo" }

func (t *TodoTool) Description() string {
	return "Plan and track work. Statuses: pending, in_progress, waiting (blocked on someone/something else, " +
		"e.g. a peer agent's reply), completed, cancelled. " +
		"Default (session) list: call with 'todos' to replace the whole list (at most one in_progress); " +
		"call with no arguments to read it back, e.g. after context was compacted. " +
		"With shared=true the list is shared across all sessions: 'todos' are upserted by id, and open items are " +
		"shown to you at the start of every turn; use it for jobs that outlive this conversation. " +
		"Use it for tasks of 3+ steps."
}

func (t *TodoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"shared": map[string]any{"type": "boolean", "description": "Use the cross-session shared list"},
			"todos": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
						"status": map[string]any{
							"type": "string",
							"enum": []string{"pending", "in_progress", "waiting", "completed", "cancelled"},
						},
					},
					"required": []string{"id", "content", "status"},
				},
			},
		},
	}
}

func (t *TodoTool) path(ctx context.Context, shared bool) string {
	if shared {
		return filepath.Join(t.dir, "shared.json")
	}
	key := ToolSessionKey(ctx)
	if key == "" {
		key = "default"
	}
	return filepath.Join(t.dir, fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:20]+".json")
}

func loadTodos(file string) []todoItem {
	var items []todoItem
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &items)
	}
	return items
}

func (t *TodoTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	shared, _ := args["shared"].(bool)
	file := t.path(ctx, shared)
	t.mu.Lock()
	defer t.mu.Unlock()

	raw, provided := args["todos"]
	if !provided || raw == nil {
		return NewToolResult(renderTodos(loadTodos(file)))
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return ErrorResult("invalid todos: " + err.Error())
	}
	var items []todoItem
	if err := json.Unmarshal(b, &items); err != nil {
		return ErrorResult("invalid todos: " + err.Error())
	}
	seen, active := map[string]bool{}, 0
	for i := range items {
		it := &items[i]
		it.Content = strings.TrimSpace(it.Content)
		if it.ID == "" || it.Content == "" {
			return ErrorResult("every item needs a non-empty id and content")
		}
		if _, ok := todoStatuses[it.Status]; !ok {
			return ErrorResult(fmt.Sprintf("item %q has invalid status %q", it.ID, it.Status))
		}
		if seen[it.ID] {
			return ErrorResult(fmt.Sprintf("duplicate id %q", it.ID))
		}
		seen[it.ID] = true
		if it.Status == "in_progress" {
			active++
		}
	}
	if shared {
		items = upsertTodos(loadTodos(file), items)
	} else if active > 1 {
		return ErrorResult("only one item may be in_progress at a time")
	}
	if len(items) > maxTodos {
		return ErrorResult(fmt.Sprintf("Too many items (%d > %d); complete or cancel some.", len(items), maxTodos))
	}
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return ErrorResult("todo write failed: " + err.Error())
	}
	out, _ := json.Marshal(items)
	if err := fileutil.WriteFileAtomic(file, out, 0o600); err != nil {
		return ErrorResult("todo write failed: " + err.Error())
	}
	return NewToolResult(renderTodos(items))
}

// upsertTodos merges updates into old by id (appending new ids), then drops
// all but the most recent keepClosed closed items.
func upsertTodos(old, updates []todoItem) []todoItem {
	idx := map[string]int{}
	for i, it := range old {
		idx[it.ID] = i
	}
	for _, u := range updates {
		if i, ok := idx[u.ID]; ok {
			old[i] = u
		} else {
			idx[u.ID] = len(old)
			old = append(old, u)
		}
	}
	closed := 0
	for _, it := range old {
		if !it.open() {
			closed++
		}
	}
	kept := old[:0:0]
	for _, it := range old {
		if !it.open() {
			if closed > keepClosed {
				closed--
				continue
			}
		}
		kept = append(kept, it)
	}
	return kept
}

// RenderSharedTodos returns the system-prompt section for open shared items, or "" if none.
func RenderSharedTodos(workspace string) string {
	var open []todoItem
	for _, it := range loadTodos(SharedTodoPath(workspace)) {
		if it.open() {
			open = append(open, it)
		}
	}
	if len(open) == 0 {
		return ""
	}
	more := 0
	if len(open) > renderMaxItems {
		more, open = len(open)-renderMaxItems, open[:renderMaxItems]
	}
	var sb strings.Builder
	sb.WriteString("# Open tasks (shared across sessions)\n\nUpdate them with the todo tool (shared=true). " +
		"[~] in progress, [?] waiting on someone else, [ ] pending.\n\n")
	for _, it := range open {
		c := it.Content
		if len(c) > 160 {
			c = c[:160] + "…"
		}
		fmt.Fprintf(&sb, "[%s] %s: %s\n", todoStatuses[it.Status], it.ID, c)
	}
	if more > 0 {
		fmt.Fprintf(&sb, "(+%d more; read them with todo shared=true)\n", more)
	}
	return sb.String()
}

func renderTodos(items []todoItem) string {
	if len(items) == 0 {
		return "(todo list is empty)"
	}
	var sb strings.Builder
	for _, it := range items {
		fmt.Fprintf(&sb, "[%s] %s: %s\n", todoStatuses[it.Status], it.ID, it.Content)
	}
	return strings.TrimRight(sb.String(), "\n")
}
