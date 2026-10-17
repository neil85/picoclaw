package tools

import (
	"strings"
	"testing"
)

func TestGuardDenialExplainsMatch(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	msg := tool.guardCommand("echo $(whoami)", t.TempDir())
	if !strings.Contains(msg, "dangerous pattern detected") || !strings.Contains(msg, "$(whoami)") ||
		!strings.Contains(msg, "Do not retry") {
		t.Fatalf("denial should name the match and discourage retries, got %q", msg)
	}
}

func TestHardlineReason(t *testing.T) {
	blocked := []string{
		"rm -rf /", "rm -rf /*", "rm -fr //", "rm -rf /./", "sudo rm -rf --no-preserve-root /",
		"rm -r -f /etc", "rm /etc -rf", "rm --recursive --force /usr/", `rm -rf "/"`,
		"rm -rf ~", "rm -rf ~/", "rm -rf $HOME", "rm -rf ${HOME}/*",
		"ls && rm -rf /", "echo hi; rm -rf /var",
		"mkfs.ext4 /dev/sda1", "sudo mkfs /dev/sdb", "dd if=/dev/zero of=/dev/sda bs=1M",
		"echo x > /dev/nvme0n1", ":(){ :|:& };:", "sudo shutdown -h now", "ls; reboot",
	}
	for _, c := range blocked {
		if HardlineReason(c) == "" {
			t.Errorf("expected hardline block: %q", c)
		}
	}

	allowed := []string{
		"rm -rf build", "rm -rf *", "rm -rf ./node_modules", "rm -rf /tmp/x", "rm file.txt", "rm /etc/hosts.bak",
		`echo "does this use mkfs?"`, `git commit -m "never dd of=/dev/sda"`, "ls -la /", "dd if=a of=b",
		"echo shutdown", "cat /dev/null > out.txt",
	}
	for _, c := range allowed {
		if r := HardlineReason(c); r != "" {
			t.Errorf("unexpected hardline block for %q: %s", c, r)
		}
	}
}
