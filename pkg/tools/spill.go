package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Tool output that exceeds SpillThreshold bytes is replaced in the model's
// context by its head and tail (errors and summaries are usually at the end),
// and the full text is saved under <workspace>/tool_output/ so nothing is lost.
const (
	SpillThreshold = 10000
	spillHead      = 3500
	spillTail      = 3000
	spillMaxFile   = 8 << 20
	spillKeep      = 72 * time.Hour
	spillDirName   = "tool_output"
)

// read_file already pages its own output; spilling it would loop (the model reads the spill file with it).
var spillExempt = map[string]bool{"read_file": true}

var spillUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// SpillLargeOutput returns content unchanged when it is small enough. Otherwise
// it returns head + omission notice + tail, and when workspace is non-empty saves
// the full text to a file the notice points to. Callers should run secret
// filtering first so the saved file never holds secrets.
// ponytail: fixed thresholds; make configurable if a deployment needs different limits.
func SpillLargeOutput(workspace, tool, callID, content string) string {
	if len(content) <= SpillThreshold || spillExempt[tool] {
		return content
	}
	head, tail := cutHead(content, spillHead), cutTail(content, spillTail)
	omitted := len(content) - len(head) - len(tail)

	note := fmt.Sprintf("[... %d bytes omitted ...]", omitted)
	if path := saveSpill(workspace, tool, callID, content); path != "" {
		note = fmt.Sprintf("[... %d bytes omitted. Full output saved to %s; inspect it with read_file "+
			"or exec grep/tail instead of re-running the command ...]", omitted, path)
	}
	return head + "\n" + note + "\n" + tail
}

func saveSpill(workspace, tool, callID, content string) string {
	if workspace == "" {
		return ""
	}
	dir := filepath.Join(workspace, spillDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	pruneSpills(dir)
	if len(content) > spillMaxFile {
		content = cutHead(content, spillMaxFile) + "\n[... file capped at 8 MiB ...]\n"
	}
	name := fmt.Sprintf("%d-%s-%s.txt", time.Now().UnixMilli(), safeName(tool), safeName(callID))
	path := filepath.Join(dir, name)
	// Plain write, no fsync: spill files are disposable, and WriteFileAtomic's sync cost ~500 ms/MB here.
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		_ = os.Remove(path) // never leave a half-written file for the model to read
		return ""
	}
	return path
}

func safeName(s string) string {
	s = spillUnsafe.ReplaceAllString(s, "_")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// pruneSpills deletes spill files older than spillKeep.
func pruneSpills(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() && time.Since(info.ModTime()) > spillKeep {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// cutHead returns at most n leading bytes without splitting a UTF-8 sequence.
func cutHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cutTail returns at most n trailing bytes without splitting a UTF-8 sequence.
func cutTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// headTailBuffer is an io.Writer that keeps only the first headMax and last
// tailMax bytes written, so a runaway command cannot exhaust memory.
type headTailBuffer struct {
	head, tail       []byte
	headMax, tailMax int
	total            int64
}

func newHeadTailBuffer(headMax, tailMax int) *headTailBuffer {
	return &headTailBuffer{headMax: headMax, tailMax: tailMax}
}

func (b *headTailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	if room := b.headMax - len(b.head); room > 0 {
		k := min(room, len(p))
		b.head = append(b.head, p[:k]...)
		p = p[k:]
	}
	if len(p) > b.tailMax { // a single huge write: only its last tailMax bytes can matter
		p, b.tail = p[len(p)-b.tailMax:], b.tail[:0]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if len(b.tail) > 2*b.tailMax { // trim lazily so appends stay amortized O(1)
			b.tail = append(b.tail[:0], b.tail[len(b.tail)-b.tailMax:]...)
		}
	}
	return n, nil
}

func (b *headTailBuffer) Len() int64 { return b.total }

func (b *headTailBuffer) String() string {
	tail := b.tail
	if len(tail) > b.tailMax {
		tail = tail[len(tail)-b.tailMax:]
	}
	omitted := b.total - int64(len(b.head)) - int64(len(tail))
	if omitted <= 0 {
		return string(b.head) + string(tail)
	}
	var sb strings.Builder
	sb.Grow(len(b.head) + len(tail) + 40)
	sb.Write(b.head)
	fmt.Fprintf(&sb, "\n... [%d bytes omitted] ...\n", omitted)
	sb.Write(tail)
	return sb.String()
}
