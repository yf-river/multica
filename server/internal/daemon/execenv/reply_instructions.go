package execenv

import "fmt"

// BuildNewCommentsHint points a resumed agent at the triggering thread before
// offering issue-wide catch-up. Comment bodies remain demand-loaded.
func BuildNewCommentsHint(issueID, triggerThreadID, newCommentsSince string, newCommentCount int) string {
	if newCommentCount <= 0 || newCommentsSince == "" || issueID == "" || triggerThreadID == "" {
		return ""
	}
	return fmt.Sprintf(
		"%d new comment(s) on this issue since your last run — don't read them all blindly. "+
			"Start with the thread your triggering comment is in: "+
			"`multica issue comment list %s --thread %s --since %s --output json` "+
			"(swap `--since` for `--tail 30` if you need the full thread, not just the delta). "+
			"Only if you need context from the other threads, catch up issue-wide: "+
			"`multica issue comment list %s --since %s --output json`.\n\n",
		newCommentCount, issueID, triggerThreadID, newCommentsSince, issueID, newCommentsSince,
	)
}

// BuildResumedCommentsHint handles a resumed session with no unseen comments.
func BuildResumedCommentsHint(issueID, triggerCommentID, triggerThreadID string) string {
	if issueID == "" || triggerThreadID == "" {
		return ""
	}
	return fmt.Sprintf(
		"You're resuming the prior session, and the triggering comment is already included above. "+
			"No other new comments on this issue since your last run. "+
			"Use the active thread anchor `%s` and triggering comment ID `%s`. "+
			"If your reply depends on thread context, do not rely only on resumed session memory — "+
			"first pull the triggering conversation with: "+
			"`multica issue comment list %s --thread %s --tail 30 --output json`.\n\n",
		triggerThreadID, triggerCommentID, issueID, triggerThreadID,
	)
}

// BuildColdCommentsHint starts a new session with bounded thread context.
func BuildColdCommentsHint(issueID, triggerThreadID string) string {
	if issueID == "" || triggerThreadID == "" {
		return ""
	}
	return fmt.Sprintf(
		"Read the triggering conversation first: "+
			"`multica issue comment list %s --thread %s --tail 30 --output json` "+
			"(that thread's root + its 30 newest replies). "+
			"Need cross-thread background? `multica issue comment list %s --recent 20 --output json`.\n\n",
		issueID, triggerThreadID, issueID,
	)
}

// BuildCommentReplyInstructions is the shared prompt/runtime-config contract
// for replying with the current trigger as parent. File input avoids shell
// rewriting and platform encoding differences.
func BuildCommentReplyInstructions(issueID, triggerCommentID string) string {
	if triggerCommentID == "" {
		return ""
	}
	writeGuidance := "Write the reply body to a UTF-8 file with your file-write tool first, then post it with `--content-file`. " +
		"Do NOT use `--content-stdin` with a HEREDOC either — when extra flags (e.g. `--assignee`, `--project` on `multica issue create`) accompany the command, the bash heredoc/flag boundary is fragile and flags can be silently swallowed into the stdin stream while the command still exits 0 (see GitHub #4182, OXY-78 / OXY-76). " +
		"The file preserves Markdown structure without forcing the reply into one shell argument.\n\n"
	removeCommand := "rm ./reply.md"
	if runtimeGOOS == "windows" {
		writeGuidance = "On Windows, write the reply body to a UTF-8 file with your file-write tool, then post it with `--content-file`. " +
			"Do NOT pipe via `--content-stdin` — Windows PowerShell 5.1's `$OutputEncoding` defaults to ASCIIEncoding when piping to native commands and silently drops non-ASCII (Chinese, Japanese, Cyrillic, accents, emoji) as `?` before the bytes reach `multica.exe`.\n\n"
		removeCommand = "Remove-Item ./reply.md"
	}
	return fmt.Sprintf(
		"If you decide to reply, post it as a comment — always use the trigger comment ID below, "+
			"do NOT reuse --parent values from previous turns in this session.\n\n"+
			"%s"+
			"Use this form, preserving the same issue ID and --parent value:\n\n"+
			"    # 1. Write the reply body to a UTF-8 file (e.g. reply.md) with your file-write tool.\n"+
			"    # 2. Post the comment. Do not add --attachment for markdown artifacts saved under the managed artifact directory; the daemon uploads those automatically.\n"+
			"    multica issue comment add %s --parent %s --content-file ./reply.md\n"+
			"    # 3. Remove the temp file so a later run does not pick up stale content:\n"+
			"    %s\n\n"+
			"Do NOT write literal `\\n` escapes to simulate line breaks; the file preserves real newlines.\n",
		writeGuidance, issueID, triggerCommentID, removeCommand,
	)
}
