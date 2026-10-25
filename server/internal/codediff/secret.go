package codediff

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Secret files are still listed: the user asked to see every change an agent
// made, including ignored files. What we refuse to do is copy their bytes into
// a database row every workspace member can read.
var secretPathPatterns = []string{
	".env",
	".env.*",
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"id_rsa*",
	"id_ed25519*",
	"*.kdbx",
	".netrc",
	".npmrc",
	".pypirc",
	"credentials",
	"credentials.*",
	"secrets",
	"secrets.*",
	"*.tfstate",
	"*.tfvars",
}

var secretPathExempt = []string{
	".env.example",
	".env.sample",
	".env.template",
}

// secretLinePatterns catch a credential inside an ordinary file (a config, a
// test fixture, a shell script). The line survives in the patch as a marker.
var secretLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|secret|password|passwd|token)["']?\s*[:=]\s*["']?[A-Za-z0-9_\-+/=]{16,}`),
}

const redactedMarker = "[redacted: potential secret]"

// IsSecretPath reports whether a relative path names a credential-bearing file.
// Only the base name is matched; a path that merely lives under a directory
// called "secrets" still follows the content rule below.
func IsSecretPath(rel string) bool {
	base := filepath.Base(rel)
	for _, exempt := range secretPathExempt {
		if ok, _ := filepath.Match(exempt, base); ok {
			return false
		}
	}
	for _, pattern := range secretPathPatterns {
		if ok, _ := filepath.Match(pattern, base); ok {
			return true
		}
	}
	if strings.Contains(filepath.ToSlash(rel), ".ssh/") {
		return true
	}
	return false
}

// RedactLine masks one patch line when it looks like it carries a credential.
func RedactLine(line string) (string, bool) {
	for _, pattern := range secretLinePatterns {
		if pattern.MatchString(line) {
			return redactedMarker, true
		}
	}
	return line, false
}

// RedactDiff 在服务端留存边界再次脱敏，兼容尚未升级的 Daemon。
func RedactDiff(diff *Diff) {
	if diff == nil {
		return
	}
	redact := func(file *File) {
		if IsSecretPath(file.Path) {
			file.Patch = ""
			file.Redacted = true
			return
		}
		file.Patch = redactPatch(file.Patch, &file.Redacted)
	}
	for i := range diff.Files {
		redact(&diff.Files[i])
	}
	for i := range diff.Roots {
		for j := range diff.Roots[i].Files {
			redact(&diff.Roots[i].Files[j])
		}
	}
}
