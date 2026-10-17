package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSpillLargeOutput(t *testing.T) {
	ws := t.TempDir()
	small := strings.Repeat("a", SpillThreshold)
	if got := SpillLargeOutput(ws, "web_fetch", "c1", small); got != small {
		t.Fatal("output at the threshold must pass through unchanged")
	}

	big := "HEAD-MARK " + strings.Repeat("é", 20000) + " TAIL-MARK"
	got := SpillLargeOutput(ws, "web_fetch", "call/1?", big)
	if len(got) > 8500 || !strings.Contains(got, "HEAD-MARK") || !strings.Contains(got, "TAIL-MARK") {
		t.Fatalf("expected a compact head+tail view, len=%d", len(got))
	}
	files, _ := filepath.Glob(filepath.Join(ws, spillDirName, "*.txt"))
	if len(files) != 1 || !strings.Contains(got, files[0]) {
		t.Fatalf("expected one spill file referenced in the notice, files=%v", files)
	}
	if data, _ := os.ReadFile(files[0]); string(data) != big {
		t.Error("spill file must hold the full output")
	}
	if strings.Contains(filepath.Base(files[0]), "?") || strings.Contains(filepath.Base(files[0]), "/") {
		t.Errorf("unsafe spill file name %q", files[0])
	}

	if got := SpillLargeOutput(ws, "read_file", "c2", big); got != big {
		t.Error("read_file output must never be spilled")
	}
	if got := SpillLargeOutput("", "x", "c3", big); strings.Contains(got, "saved to") || len(got) > 8500 {
		t.Error("no workspace: bounded output, no file pointer")
	}

	// old spill files are pruned on the next spill.
	old := filepath.Join(ws, spillDirName, "0-old-old.txt")
	_ = os.WriteFile(old, []byte("x"), 0o600)
	past := time.Now().Add(-spillKeep - time.Hour)
	_ = os.Chtimes(old, past, past)
	SpillLargeOutput(ws, "web_fetch", "c4", big)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("expired spill file should be removed")
	}
}

func TestHeadTailBuffer(t *testing.T) {
	b := newHeadTailBuffer(100, 100)
	chunk := strings.Repeat("0123456789", 100) // 1000 bytes
	for i := 0; i < 1000; i++ {                // 1 MB total
		b.Write([]byte(chunk))
	}
	b.Write([]byte("THE-END"))
	if cap(b.tail) > 10*100 || len(b.head) != 100 { // ~1 MB was written; a few hundred bytes retained
		t.Errorf("memory not bounded: head=%d tailcap=%d", len(b.head), cap(b.tail))
	}
	s := b.String()
	if !strings.HasPrefix(s, "0123456789") || !strings.HasSuffix(s, "THE-END") || !strings.Contains(s, "bytes omitted") {
		t.Errorf("unexpected content: %q", s)
	}
	if b.Len() != 1000*1000+7 {
		t.Errorf("total = %d", b.Len())
	}
	small := newHeadTailBuffer(100, 100)
	small.Write([]byte("short"))
	if small.String() != "short" {
		t.Errorf("small output must be verbatim, got %q", small.String())
	}
}

// A failing command with huge output must keep its tail (errors, exit code), not just the head.
func TestExecKeepsTailOfHugeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	ws := t.TempDir()
	tool, err := NewExecTool(ws, false)
	if err != nil {
		t.Fatal(err)
	}
	res := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": "seq 1 300000; echo FINAL-ERROR >&2; exit 3",
	})
	if !res.IsError || !strings.Contains(res.ForLLM, "FINAL-ERROR") || !strings.Contains(res.ForLLM, "exited with code 3") {
		t.Fatalf("tail lost:\n%.300s...\n%s", res.ForLLM, res.ForLLM[max(0, len(res.ForLLM)-200):])
	}
	if len(res.ForLLM) > 9000 {
		t.Errorf("result too large for context: %d", len(res.ForLLM))
	}
	if files, _ := filepath.Glob(filepath.Join(ws, spillDirName, "*.txt")); len(files) != 1 {
		t.Errorf("expected one spill file, got %v", files)
	}
}
