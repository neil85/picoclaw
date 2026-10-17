package tools

import (
	"regexp"
	"strings"
)

// Hardline rules block catastrophic commands unconditionally: they are not
// affected by tools.exec.enable_deny_patterns, custom allow patterns, or tool
// approval. Anything merely risky belongs in the (configurable) deny patterns.

var hardlineRules = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`(?:^|[;&|(]\s*)(?:sudo\s+)?mkfs(?:\.[a-z0-9]+)?\b`), "format filesystem (mkfs)"},
	{
		regexp.MustCompile(`(?:^|[;&|(]\s*)(?:sudo\s+)?dd\b[^\n]*\bof=/dev/(?:sd|hd|vd|xvd|nvme|mmcblk)`),
		"dd to raw block device",
	},
	{regexp.MustCompile(`>\s*/dev/(?:sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d)`), "write to raw block device"},
	{regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`), "fork bomb"},
	{
		regexp.MustCompile(`(?:^|[;&|(]\s*)(?:sudo\s+)?(?:shutdown|reboot|poweroff|halt)\b`),
		"shutdown/reboot the machine",
	},
}

// rmProtectedTargets are paths that must never be removed recursively.
var rmProtectedTargets = map[string]bool{
	"/": true, "~": true, "$home": true, "${home}": true,
	"/bin": true, "/boot": true, "/dev": true, "/etc": true, "/home": true, "/lib": true, "/lib64": true,
	"/opt": true, "/proc": true, "/root": true, "/sbin": true, "/srv": true, "/sys": true, "/usr": true, "/var": true,
}

var commandSeparators = regexp.MustCompile(`&&|\|\||[;|&\n()]`)

// HardlineReason reports why command must never run, or "" if it is not a
// hardline command.
func HardlineReason(command string) string {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, rule := range hardlineRules {
		if rule.re.MatchString(lower) {
			return rule.reason
		}
	}
	for _, segment := range commandSeparators.Split(lower, -1) {
		if isProtectedRecursiveRm(strings.Fields(segment)) {
			return "recursive delete of root, home or system directory"
		}
	}
	return ""
}

func isProtectedRecursiveRm(fields []string) bool {
	for len(fields) > 0 && (fields[0] == "sudo" || fields[0] == "command" || fields[0] == "exec") {
		fields = fields[1:]
	}
	if len(fields) == 0 || fields[0] != "rm" {
		return false
	}
	recursive := false
	for _, f := range fields[1:] {
		switch {
		case f == "--recursive":
			recursive = true
		case strings.HasPrefix(f, "--"):
		case strings.HasPrefix(f, "-"):
			recursive = recursive || strings.Contains(f, "r")
		}
	}
	if !recursive {
		return false
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue
		}
		if rmProtectedTargets[normalizeRmTarget(f)] {
			return true
		}
	}
	return false
}

// normalizeRmTarget strips quotes, trailing "/" and "/*" and collapses "/." so
// `"/"`, `/*`, `/./` and `~/` all map to a protected key.
func normalizeRmTarget(p string) string {
	p = strings.Trim(p, `"'`)
	if p == "" || !strings.ContainsAny(p[:1], "/~$") {
		return p // relative paths such as "*" or "build/" are never protected
	}
	for {
		trimmed := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(p, "*"), "/."), "/")
		if trimmed == p {
			break
		}
		p = trimmed
	}
	if p == "" {
		return "/" // "/", "//", "/*" and friends reduce to empty
	}
	return p
}
