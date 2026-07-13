package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setRuntimeGOOS(t *testing.T, goos string) {
	t.Helper()
	saved := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = saved })
	runtimeGOOS = goos
}

func injectedRuntimeConfigContent(t *testing.T, provider string, ctx TaskContextForEnv) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := InjectRuntimeConfig(dir, provider, ctx); err != nil {
		t.Fatalf("InjectRuntimeConfig failed: %v", err)
	}
	fileName := "CLAUDE.md"
	if provider != "claude" {
		fileName = "AGENTS.md"
	}
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("read %s: %v", fileName, err)
	}
	return fileName, string(data)
}

// TestBuildCommentReplyInstructionsCodexLinux pins that the Linux/macOS
// reply template now mandates `--content-file` (post-#4182). The previous
// `--content-stdin` + HEREDOC mandate (#1795 / #1851 / MUL-2904) was kept
// for years to defend against backtick / `$()` substitution in the body,
// but the heredoc/flag boundary turned out to be fragile in its own right:
// when a model wrapped extra flags around the heredoc on `multica issue
// create`, the flags got swallowed into stdin and silently dropped (OXY-78,
// OXY-76). The file path defeats both classes — the body never reaches the
// shell, and all flags live on one shell-token line.
//
// Not parallel: mutates the package-level runtimeGOOS.
func TestBuildCommentReplyInstructionsLinux(t *testing.T) {
	setRuntimeGOOS(t, "linux")

	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"

	got := BuildCommentReplyInstructions(issueID, triggerID)

	for _, want := range []string{
		"multica issue comment add " + issueID + " --parent " + triggerID + " --content-file ./reply.md",
		"Write the reply body to a UTF-8 file",
		"`--content-file`",
		"#4182",
		"rm ./reply.md",
		"Do NOT write literal `\\n` escapes to simulate line breaks",
		"do NOT reuse --parent values from previous turns",
		"Do not add --attachment for markdown artifacts saved under the managed artifact directory",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("linux reply instructions missing %q\n---\n%s", want, got)
		}
	}

	for _, banned := range []string{
		"--content \"...\"",
		"<<'COMMENT'",
		"cat <<",
		"--parent " + triggerID + " --content-stdin",
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("linux reply instructions should not contain %q\n---\n%s", banned, got)
		}
	}
}

// TestBuildCommentReplyInstructionsWindowsUsesContentFile pins that Windows
// gets the `--content-file` template. Piping through PowerShell loses non-ASCII
// bytes (PS 5.1's `$OutputEncoding` defaults to ASCIIEncoding). Issues
// #2198 (Chinese, Codex), #2236 (Chinese, Codex), #2376 (Cyrillic,
// non-Codex agent name) all match this signature.
//
// Not parallel: mutates the package-level runtimeGOOS.
func TestBuildCommentReplyInstructionsWindowsUsesContentFile(t *testing.T) {
	setRuntimeGOOS(t, "windows")

	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"

	got := BuildCommentReplyInstructions(issueID, triggerID)
	for _, want := range []string{
		"multica issue comment add " + issueID + " --parent " + triggerID + " --content-file",
		"On Windows, write the reply body to a UTF-8 file",
		"Do NOT pipe via `--content-stdin`",
		"silently drops non-ASCII",
		"$OutputEncoding",
		"Do not add --attachment for markdown artifacts saved under the managed artifact directory",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("windows reply instructions missing %q\n---\n%s", want, got)
		}
	}
	for _, banned := range []string{
		"<<'COMMENT'",
		"--parent " + triggerID + " --content-stdin",
		"cat <<",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("windows reply instructions should not contain %q\n---\n%s", banned, got)
		}
	}
}

func TestBuildCommentReplyInstructionsEmptyWhenNoTrigger(t *testing.T) {
	t.Parallel()

	if got := BuildCommentReplyInstructions("issue-id", ""); got != "" {
		t.Fatalf("expected empty string when triggerCommentID is empty, got %q", got)
	}
}

// Pins runtimeGOOS to "linux" so the helper output is deterministic.
// Provider is "claude" — exercises the non-codex inline path through
// InjectRuntimeConfig end-to-end. Not parallel: mutates runtimeGOOS.
func TestInjectRuntimeConfigCommentTriggerUsesHelper(t *testing.T) {
	setRuntimeGOOS(t, "linux")

	dir := t.TempDir()

	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"

	ctx := TaskContextForEnv{
		IssueID:          issueID,
		TriggerCommentID: triggerID,
	}
	if _, err := InjectRuntimeConfig(dir, "claude", ctx); err != nil {
		t.Fatalf("InjectRuntimeConfig failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}

	s := string(content)
	for _, want := range []string{
		triggerID,
		"multica issue comment add " + issueID + " --parent " + triggerID,
		"do NOT reuse --parent values from previous turns",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("CLAUDE.md missing %q", want)
		}
	}
}

// TestInjectRuntimeConfigWindowsCommentTriggerHasNoStdin asserts the
// end-to-end CLAUDE.md / AGENTS.md surface for a comment-triggered task on
// a Windows daemon — across Codex and non-Codex providers — has no
// prescriptive `--content-stdin` directive that could steer the agent at
// the broken Windows pipe path.
//
// Not parallel: mutates the package-level runtimeGOOS.
func TestInjectRuntimeConfigWindowsCommentTriggerHasNoStdin(t *testing.T) {
	setRuntimeGOOS(t, "windows")

	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"
	ctx := TaskContextForEnv{
		IssueID:          issueID,
		TriggerCommentID: triggerID,
	}

	for _, provider := range []string{"claude", "codex", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			fileName, s := injectedRuntimeConfigContent(t, provider, ctx)

			for _, want := range []string{
				"multica issue comment add " + issueID + " --parent " + triggerID + " --content-file",
				"--content-file",
				"On Windows, write the reply body to a UTF-8 file",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("%s missing %q\n---\n%s", fileName, want, s)
				}
			}

			// Prescriptive stdin directives must NOT appear anywhere in
			// the Windows surface. Pin sentence-level substrings (not
			// bare flag names) so anti-prescriptive prose like "do NOT
			// pipe via `--content-stdin`" doesn't trip the ban.
			for _, banned := range []string{
				"--parent " + triggerID + " --content-stdin",
				"always use `--content-stdin` with a HEREDOC, even for short single-line replies",
				"MUST pipe via stdin",
				"use `--description-stdin` and pipe a HEREDOC",
				"<<'COMMENT'",
				"Agent-authored comments should always pipe content via stdin",
			} {
				if strings.Contains(s, banned) {
					t.Errorf("%s still steers agent at stdin: %q\n---\n%s", fileName, banned, s)
				}
			}
		})
	}
}

// TestInjectRuntimeConfigWindowsAssignmentBriefStaysFileOnly pins the PR #3654
// review fix: on Windows, the ASSIGNMENT-triggered brief must never *recommend*
// `--content-stdin`. Unlike the comment-trigger path, the assignment workflow
// has no BuildCommentReplyInstructions override, so an agent that follows the
// "post your final results" step literally would pipe its final comment through
// PowerShell and drop non-ASCII bytes (#2198 / #2236 / #2376). The OS-aware
// ## Comment Formatting section (file-only on Windows) is the single source of
// truth; the Available Commands entry and step 6 must defer to it, not re-offer
// stdin. The flag synopsis may still *list* `--content-stdin` as available.
//
// Not parallel: mutates the package-level runtimeGOOS.
func TestInjectRuntimeConfigWindowsAssignmentBriefStaysFileOnly(t *testing.T) {
	setRuntimeGOOS(t, "windows")

	// Assignment-triggered: IssueID set, no TriggerCommentID.
	ctx := TaskContextForEnv{IssueID: "issue-1"}

	for _, provider := range []string{"claude", "codex", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			fileName, s := injectedRuntimeConfigContent(t, provider, ctx)

			// The Windows Comment Formatting section is file-only.
			for _, want := range []string{
				"## Comment Formatting",
				"On Windows, **always write the comment body to a UTF-8 file",
				"do NOT pipe via `--content-stdin`",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("%s missing Windows file-only guidance %q\n---\n%s", fileName, want, s)
				}
			}

			// No prose may RECOMMEND stdin on Windows. The flag synopsis may
			// still list `--content-stdin`; only the prescriptive "file or
			// stdin" phrasings are banned.
			for _, banned := range []string{
				"or `--content-stdin`",
				"using `--content-file` or `--content-stdin`",
				"use `--content-file <path>` or `--content-stdin`",
			} {
				if strings.Contains(s, banned) {
					t.Errorf("%s recommends stdin on Windows: %q\n---\n%s", fileName, banned, s)
				}
			}
		})
	}
}
