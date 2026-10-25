// 文件职责：定义 `multica issue` 整棵 CLI 命令树——issue 的增删改查、状态/排序、
// 指派、评论、订阅人、执行历史与用量、搜索等子命令。命令层只负责把用户参数拼成
// HTTP 请求发给 server、再把结果以表格/JSON 形式打印；文末的 assignee 解析辅助
// 函数被 issue/project 等多组命令共用，是「人名/ID 如何映射成 assignee_type+ID」的
// 公共逻辑所在。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/util"
)

// resolveTextFlag 从三种互斥的取值来源中挑出一个文本型 flag 的值：
// 行内 `--<name>`、`--<name>-stdin`（标准输入）、`--<name>-file <path>`（读文件），
// 沿用既有 `--content` / `--content-stdin` 的模式。返回 (解析出的文本, 是否确实
// 提供了文本, 错误)。超过一个来源被设置时报错；要求 stdin/文件却没有内容（去掉
// 末尾换行后为空串）也报错。
//
// 行内值会经 util.UnescapeBackslashEscapes 处理，使 bash 双引号里的 `\n` 变成真
// 换行；stdin 与文件内容则原样返回，保证字面反斜杠不被破坏。
//
// 之所以提供 `-file` 来源：Windows 下 agent 用 PowerShell 管道向
// `--<name>-stdin` 传 HEREDOC 内容时，非 ASCII 字节会被静默丢弃（PowerShell
// 5.1 向原生命令管道时 `$OutputEncoding` 默认 ASCII 编码），中文/西里尔文等一律
// 变成 `?`。直接读 UTF-8 文件绕开了 shell 的管道重编码。参见 issues #2198 / #2236 / #2376。
func resolveTextFlag(cmd *cobra.Command, flagName string) (string, bool, error) {
	stdinFlag := flagName + "-stdin"
	fileFlag := flagName + "-file"
	useStdin, _ := cmd.Flags().GetBool(stdinFlag)
	inline, _ := cmd.Flags().GetString(flagName)
	filePath, _ := cmd.Flags().GetString(fileFlag)

	sources := 0
	if useStdin {
		sources++
	}
	if inline != "" {
		sources++
	}
	if filePath != "" {
		sources++
	}
	if sources > 1 {
		return "", false, fmt.Errorf("--%s, --%s, and --%s are mutually exclusive", flagName, stdinFlag, fileFlag)
	}

	if useStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", false, fmt.Errorf("read stdin for --%s: %w", stdinFlag, err)
		}
		body := strings.TrimSuffix(string(data), "\n")
		if body == "" {
			return "", false, fmt.Errorf("stdin content for --%s is empty", stdinFlag)
		}
		return body, true, nil
	}
	if filePath != "" {
		if err := ensureFileFlagWithinWorkdir(cmd, fileFlag, flagName, filePath); err != nil {
			return "", false, err
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", false, fmt.Errorf("read file for --%s: %w", fileFlag, err)
		}
		body := strings.TrimSuffix(string(data), "\n")
		if body == "" {
			return "", false, fmt.Errorf("file content for --%s is empty", fileFlag)
		}
		return body, true, nil
	}
	if inline == "" {
		return "", false, nil
	}
	return util.UnescapeBackslashEscapes(inline), true, nil
}

// ensureFileFlagWithinWorkdir 采用「失败即关闭」策略：当 `--<name>-file` 指向的
// 路径解析后位于当前工作目录之外，一律拒绝，除非显式传了 --allow-external-file。
//
// 原因：agent 的任务工作目录按「profile×任务」彼此隔离，但 /tmp 这类机器级共享
// 路径并不隔离。MUL-4252 曾追查到一次跨环境串数据正出自这里：某次 quick-create
// 把描述写进固定的 /tmp/desc.md，写入因另一个环境的运行几分钟前留下的陈旧文件而
// 静默失败，随后 --description-file 就把那份旧内容喂给了新 issue。要求文件必须
// 落在工作目录下，等于把「悄悄读到别的运行的文件」变成一声响亮的命令报错——
// 「内容不对」的 bug 变成了「命令失败」的 bug。
func ensureFileFlagWithinWorkdir(cmd *cobra.Command, fileFlag, flagName, filePath string) error {
	if allow, _ := cmd.Flags().GetBool("allow-external-file"); allow {
		return nil
	}
	within, err := fileWithinWorkingDir(filePath)
	if err != nil {
		return fmt.Errorf("resolve --%s path %q: %w", fileFlag, filePath, err)
	}
	if !within {
		if !pathExists(filePath) {
			return fmt.Errorf(
				"--%s path %q does not exist; it also resolves outside the current working directory, "+
					"so --allow-external-file would not make this read succeed. Write the file inside the "+
					"task workdir (e.g. ./%s.md) and pass that path.",
				fileFlag, filePath, flagName)
		}
		return fmt.Errorf(
			"--%s path %q resolves outside the current working directory; "+
				"write agent temp files inside the task workdir (e.g. ./%s.md) rather than machine-shared "+
				"paths like /tmp, where another run's stale file can be read by mistake. "+
				"Pass --allow-external-file to override.",
			fileFlag, filePath, flagName)
	}
	return nil
}

// pathExists reports whether filePath names something on disk. It exists to
// separate the two facts the containment guard used to merge: a rejected path
// that is ALSO missing has no stale file behind it for anyone to read by
// mistake, so leading with --allow-external-file sends the caller to disable
// the guard and hit an unrelated not-found error on the retry. Any stat error
// other than "not exists" (a permission wall, a broken mount) is treated as
// "exists" so the guard keeps its own wording and stays fail-closed.
func pathExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return !errors.Is(err, os.ErrNotExist)
}

// fileWithinWorkingDir reports whether filePath resolves to a location inside
// the process working directory. Both sides are canonicalized the same way, so
// aliased roots (e.g. macOS /tmp -> /private/tmp), symlinks planted inside the
// workdir, and directory junctions pointing out of it all fail closed. A path
// that does not exist yet is resolved as far as it exists, so the caller's
// os.ReadFile still surfaces the real not-found error afterwards.
//
// Windows relative paths are handed over for what they are, not for what they
// look like: `\tmp\desc.md` resolves against the workdir's volume ROOT and
// `C:tmp\desc.md` against the current directory on C:, so prefixing the
// workdir onto either would judge a shadow file while os.ReadFile opens the
// real one. util.ResolveSymlinksBestEffort preserves those kinds, for link
// targets as well as for the input itself.
func fileWithinWorkingDir(filePath string) (bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	base, err := util.ResolveSymlinksBestEffort(cwd)
	if err != nil {
		// The workdir's own resolution cannot be determined (an unobservable
		// drive, a reparse point no query can name). Comparing a candidate
		// against a root that cannot be named judges whatever string the
		// resolver would have guessed, so fail closed.
		return false, nil
	}
	// Canonicalize the candidate exactly the way the base was, and hand it over
	// as typed: util.ResolveSymlinksBestEffort makes it absolute itself, because
	// pre-joining it here would clean the string first and collapse a ".."
	// across a symlink — judging a different path than the one os.ReadFile then
	// opens.
	//
	// os.Getwd() returns the LOGICAL working directory when $PWD names it (a
	// shell's `cd`, and the PWD the daemon exports to agent processes, both
	// carry unresolved symlinks), so the two sides only agree once both are
	// resolved. Resolving best-effort is what makes that possible for a
	// candidate that does not exist yet — a typo, or an artifact a build step
	// never produced — and every cheaper approximation gets one direction wrong:
	//
	//   - Leaving the candidate unresolved (filepath.Clean) reads a path inside
	//     the workdir as outside it, which reports a missing file as a
	//     guardrail violation and points the caller at --allow-external-file
	//     instead of at the real error.
	//   - Resolving only one level up (filepath.Dir) still breaks once an
	//     intermediate directory is missing, and worse: with an already-
	//     canonical cwd it reads `escape/sub/x.md` — under a symlink pointing
	//     out of the workdir — as INSIDE it, because the unresolvable tail
	//     falls back to a lexical clean that never sees the symlink.
	abs, err := util.ResolveSymlinksBestEffort(filePath)
	if err != nil {
		// ErrUnresolvablePath: the kernel may open this path somewhere this
		// process cannot name — a reparse point that redirects but survives
		// both os.Readlink and a handle query, or a drive-relative path on a
		// drive whose current directory is unobservable. The string the input
		// spells is not evidence of where the kernel would read, so fail
		// closed; the caller's own os.ReadFile would surface a plain error
		// for the same path.
		return false, nil
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		// Two absolute paths that filepath.Rel cannot relate are on different
		// volumes, which is as far outside the workdir as a path can get. Only
		// Windows produces this: with a workdir on C:, `--content-file
		// Z:\report.md` used to surface `Rel: can't make Z:\report.md relative
		// to C:\...` as an internal resolve failure instead of the guardrail's
		// own explanation — the same misleading-diagnosis shape this guard is
		// being fixed for. Measured on 10.0.19045 / go1.26.6. There is no Unix
		// input that reaches this branch, so the windows-tagged test in this
		// package is the only thing that covers it.
		return false, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

// issueCmd 是 `multica issue` 父命令，下面挂着 list/get/create/update/comment 等
// 子命令（见 init()）。它本身只负责分组，不执行任何动作。
var issueCmd = &cobra.Command{
	Use:   "issue",
	Short: "Work with issues",
}

var issueListCmd = &cobra.Command{
	Use:   "list",
	Short: "List issues in the workspace",
	RunE:  runIssueList,
}

var issueGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get issue details",
	Args:  exactArgs(1),
	RunE:  runIssueGet,
}

var issuePullRequestsCmd = &cobra.Command{
	Use:     "pull-requests <id>",
	Aliases: []string{"prs"},
	Short:   "List pull requests linked to an issue",
	Args:    exactArgs(1),
	RunE:    runIssuePullRequests,
}

var issueSourceFetchCmd = &cobra.Command{
	Use:   "source-fetch <id>",
	Short: "记录或抓取任务的外部来源",
	Args:  exactArgs(1),
	RunE:  runIssueSourceFetch,
}

var issueChildrenCmd = &cobra.Command{
	Use:     "children <id>",
	Aliases: []string{"subissues"},
	Short:   "List an issue's sub-issues grouped by stage",
	Args:    exactArgs(1),
	RunE:    runIssueChildren,
}

var issueCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new issue",
	RunE:  runIssueCreate,
}

var issueUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an issue",
	Args:  exactArgs(1),
	RunE:  runIssueUpdate,
}

var issueAssignCmd = &cobra.Command{
	Use:   "assign <id>",
	Short: "Assign an issue to a member, agent, or squad",
	Args:  exactArgs(1),
	RunE:  runIssueAssign,
}

var issueStatusCmd = &cobra.Command{
	Use:   "status <id> <status>",
	Short: "Change issue status",
	Long: "Change an issue's status. The argument is a status KEY, not its display name.\n" +
		"Built-in keys: backlog, todo, in_progress, in_review, done, blocked, cancelled.\n" +
		"A workspace may define custom statuses on top of these; their keys are shown in\n" +
		"Workspace Settings > Issue Statuses, and an unknown value errors with the full list.",
	Args: exactArgs(2),
	RunE: runIssueStatus,
}

var issueReorderCmd = &cobra.Command{
	Use:   "reorder <id>",
	Short: "Move an issue within its status column",
	Long: "Reposition an issue inside its current status column by computing a new\n" +
		"ordering position, the same value the board's drag-and-drop sets.\n\n" +
		"Pick exactly one target:\n" +
		"  --before <id>  place it directly above another issue in the same column\n" +
		"  --after  <id>  place it directly below another issue in the same column\n" +
		"  --top          move it to the top of its column\n" +
		"  --bottom       move it to the bottom of its column\n\n" +
		"Reorder stays inside the issue's current column. To move an issue to a\n" +
		"different column, change its status first with `multica issue status`,\n" +
		"which lands it at the top of the destination column — no follow-up\n" +
		"reorder needed.",
	Args: exactArgs(1),
	RunE: runIssueReorder,
}

// Comment 子命令组：评论的列出/新增/删除/解决/取消解决。
// 注意 use 里的 <issue-id>/<comment-id> 参数指的是哪种 ID，见各命令的 RunE。

var issueCommentCmd = &cobra.Command{
	Use:   "comment",
	Short: "Work with issue comments",
}

var issueCommentListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List comments on an issue",
	Args:  exactArgs(1),
	RunE:  runIssueCommentList,
}

var issueCommentAddCmd = &cobra.Command{
	Use:   "add <issue-id>",
	Short: "Add a comment to an issue",
	Args:  exactArgs(1),
	RunE:  runIssueCommentAdd,
}

var issueCommentUpdateCmd = &cobra.Command{
	Use:   "update <comment-id>",
	Short: "Update a comment",
	Long: "Update a comment you authored. Workspace owners and admins can update any comment.\n\n" +
		"Pass the revision returned by `multica issue comment list <issue-id> --output json`; " +
		"the update is rejected if another editor changed the comment first. Changing the content " +
		"uses the same agent-trigger behavior as editing the comment in the app.",
	Args: exactArgs(1),
	RunE: runIssueCommentUpdate,
}

var issueCommentDeleteCmd = &cobra.Command{
	Use:   "delete <comment-id>",
	Short: "Delete a comment",
	Args:  exactArgs(1),
	RunE:  runIssueCommentDelete,
}

var issueCommentResolveCmd = &cobra.Command{
	Use:   "resolve <comment-id>",
	Short: "Resolve a comment thread",
	Args:  exactArgs(1),
	RunE:  runIssueCommentResolve,
}

var issueCommentUnresolveCmd = &cobra.Command{
	Use:   "unresolve <comment-id>",
	Short: "Unresolve a comment thread",
	Args:  exactArgs(1),
	RunE:  runIssueCommentUnresolve,
}

// Subscriber 子命令组：issue 订阅者的查看/添加/移除。

var issueSubscriberCmd = &cobra.Command{
	Use:   "subscriber",
	Short: "Work with issue subscribers",
}

var issueSubscriberListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List subscribers of an issue",
	Args:  exactArgs(1),
	RunE:  runIssueSubscriberList,
}

var issueSubscriberAddCmd = &cobra.Command{
	Use:   "add <issue-id>",
	Short: "Subscribe a user or agent to an issue (defaults to the caller)",
	Args:  exactArgs(1),
	RunE:  runIssueSubscriberAdd,
}

var issueSubscriberRemoveCmd = &cobra.Command{
	Use:   "remove <issue-id>",
	Short: "Unsubscribe a user or agent from an issue (defaults to the caller)",
	Args:  exactArgs(1),
	RunE:  runIssueSubscriberRemove,
}

// Execution history 子命令组：查看 issue 上的执行历史（runs）、单次执行的消息、
// 聚合 token 用量，以及重跑/取消任务。

// headerActiveRunsTruncated mirrors handler.HeaderActiveRunsTruncated. Declared
// as a literal rather than imported because the CLI does not depend on the
// handler package (same convention as headerTimelineTruncated in
// `issue timeline`).
const headerActiveRunsTruncated = "X-Active-Runs-Truncated"

var issueRunsCmd = &cobra.Command{
	Use:   "runs <issue-id>",
	Short: "List execution history for an issue",
	Long: "List agent task runs for an issue.\n\n" +
		"Defaults to this issue's full execution history, newest first. Narrow it to " +
		"work in flight with --active, or widen it across the sub-issue family with " +
		"--siblings when you need to know whether another agent is already working " +
		"next to you.",
	Args: exactArgs(1),
	RunE: runIssueRuns,
}

var issueRunMessagesCmd = &cobra.Command{
	Use:   "run-messages <task-id>",
	Short: "List messages for an execution",
	Args:  exactArgs(1),
	RunE:  runIssueRunMessages,
}

var issueUsageCmd = &cobra.Command{
	Use:   "usage <issue-id>",
	Short: "Show aggregated token usage for an issue",
	Args:  exactArgs(1),
	RunE:  runIssueUsage,
}

var issueRerunCmd = &cobra.Command{
	Use:   "rerun <id>",
	Short: "Re-enqueue an issue's current agent assignment as a fresh task",
	Args:  exactArgs(1),
	RunE:  runIssueRerun,
}

var issueCancelTaskCmd = &cobra.Command{
	Use:   "cancel-task <task-id>",
	Short: "Cancel a running or queued task (interrupts in-flight agent)",
	Long: "Cancel a single task by its ID. Accepts the short ID prefix shown by `issue runs`. " +
		"Use --issue to scope short-ID resolution to a specific issue when ambiguous. " +
		"Triggers daemon-side interrupt of any in-flight agent so it stops emitting tool calls promptly.",
	Args: exactArgs(1),
	RunE: runIssueCancelTask,
}

var issueSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search issues by title, description, or comments",
	Long: "Search issues in the current workspace. The query matches issue titles,\n" +
		"descriptions, and comment bodies, so a conclusion that only lives in a\n" +
		"comment thread is still findable.\n\n" +
		"A bare number or an identifier-shaped query (\"412\", \"AGE-412\") matches\n" +
		"the issue with that number. The prefix is not validated, and number\n" +
		"matches rank first, so pasting an identifier from another tracker can\n" +
		"put an unrelated local issue at the top of the results.\n\n" +
		"The MATCH column (match_source in --output json) reports the strongest\n" +
		"field that matched — title, description, or comment — and falls back to\n" +
		"\"comment\" for a number-only hit. Treat it as a display hint, not a\n" +
		"filter.",
	Args: cobra.ExactArgs(1),
	RunE: runIssueSearch,
}

// validIssueStatuses 是 7 个「内置」状态 key，每个工作区都一定有它们。自 MUL-6243
// 起工作区还可以自定义额外状态，所以这份列表只用于帮助文本和报错信息，不代表
// 可接受值的全集——真正接受哪些由 validateIssueStatus 说了算。
var validIssueStatuses = []string{
	"backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled",
}

// validIssuePriorities 是 issue 任务安排四象限的合法取值列表。
var validIssuePriorities = []string{
	"important_urgent", "important_not_urgent", "not_important_urgent", "not_important_not_urgent",
}

// issueListMaxPageSize mirrors the page clamp in the server's ListIssues
// handler; the two must move together.
const issueListMaxPageSize = 100

// validIssueSortColumns are the sort keys `issue list --sort` accepts. They
// mirror the server's ListIssues handler. "position" is the default and is
// always sorted ascending (the board's manual drag order), so --direction is
// only meaningful for the other columns.
var validIssueSortColumns = []string{
	"position", "title", "created_at", "start_date", "due_date", "priority",
}

// validIssueFields are the top-level keys /api/issues actually emits for
// `issue list` — i.e. the JSON tags of handler.IssueResponse
// (server/internal/handler/issue.go), minus reactions/attachments/
// source_context, which are omitempty and never set by the list endpoint
// (detail-only). TestValidIssueFieldsMatchListEndpointShape guards this
// against drifting from that struct. There is no plain "assignee" field —
// it is split into assignee_type/assignee_id — so that name is rejected
// rather than silently ignored.
var validIssueFields = []string{
	"id", "workspace_id", "number", "identifier", "title", "description",
	"status", "status_category", "status_name", "priority", "assignee_type",
	"assignee_id", "creator_type", "creator_id", "parent_issue_id",
	"project_id", "position", "stage", "start_date", "due_date", "created_at",
	"updated_at", "revision", "last_activity_at", "metadata", "properties",
	"labels",
}

// directionalIssueSortColumns are the sort keys for which --direction is
// meaningful: every valid column except "position". Derived from
// validIssueSortColumns so the two stay in sync.
var directionalIssueSortColumns = func() []string {
	cols := make([]string, 0, len(validIssueSortColumns)-1)
	for _, c := range validIssueSortColumns {
		if c != "position" {
			cols = append(cols, c)
		}
	}
	return cols
}()

// validateIssueStatus 只校验状态 key 的「形态」，不校验它是否在合法集合里。
//
// 自 MUL-6243 起工作区可以自定义状态，因此 CLI 不经过一次网络往返就无法知道
// 合法全集。它先在本地校验格式——足以立刻拦下常见的拼写错误，且完全离线——
// 至于未知 key 则交给 server 拒绝（server 会以 400 列出该工作区的真实状态）。
// 若在这里硬编码一份列表，反而会拒绝掉这个功能本要支持的自定义状态。
func validateIssueStatus(status string) error {
	trimmed := strings.ToLower(strings.TrimSpace(status))
	if trimmed == "" {
		return fmt.Errorf("invalid status %q; valid values: %s",
			status, strings.Join(validIssueStatuses, ", "))
	}
	if !issueStatusKeyPattern.MatchString(trimmed) {
		return fmt.Errorf(
			"invalid status %q; a status key is 1-32 characters of lowercase letters, digits or underscore. Built-in values: %s",
			status, strings.Join(validIssueStatuses, ", "))
	}
	return nil
}

// issueStatusKeyPattern 与 issue_status.key 的存储约束保持一致：小写字母/数字开头，
// 1-32 位小写字母、数字或下划线。
var issueStatusKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)

// validateIssuePriority 校验 --priority 是否为合法枚举值。
func validateIssuePriority(priority string) error {
	return validateIssueEnum("priority", priority, validIssuePriorities)
}

// validateIssueEnum 是通用的枚举校验：value 必须出现在 allowed 列表里，否则报错
// 并把合法值一并列出。
func validateIssueEnum(field, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q; valid values: %s", field, value, strings.Join(allowed, ", "))
}

func init() {
	issueCmd.AddCommand(issueListCmd)
	issueCmd.AddCommand(issueGetCmd)
	issueCmd.AddCommand(issuePullRequestsCmd)
	issueCmd.AddCommand(issueSourceFetchCmd)
	issueCmd.AddCommand(issueChildrenCmd)
	issueCmd.AddCommand(issueCreateCmd)
	issueCmd.AddCommand(issueUpdateCmd)
	issueCmd.AddCommand(issueAssignCmd)
	issueCmd.AddCommand(issueStatusCmd)
	issueCmd.AddCommand(issueReorderCmd)
	issueCmd.AddCommand(issueCommentCmd)
	issueCmd.AddCommand(issueSubscriberCmd)
	issueCmd.AddCommand(issueRunsCmd)
	issueCmd.AddCommand(issueRunMessagesCmd)
	issueCmd.AddCommand(issueUsageCmd)
	issueCmd.AddCommand(issueRerunCmd)
	issueCmd.AddCommand(issueCancelTaskCmd)
	issueCmd.AddCommand(issueSearchCmd)

	issueCommentCmd.AddCommand(issueCommentListCmd)
	issueCommentCmd.AddCommand(issueCommentAddCmd)
	issueCommentCmd.AddCommand(issueCommentUpdateCmd)
	issueCommentCmd.AddCommand(issueCommentDeleteCmd)
	issueCommentCmd.AddCommand(issueCommentResolveCmd)
	issueCommentCmd.AddCommand(issueCommentUnresolveCmd)

	issueSubscriberCmd.AddCommand(issueSubscriberListCmd)
	issueSubscriberCmd.AddCommand(issueSubscriberAddCmd)
	issueSubscriberCmd.AddCommand(issueSubscriberRemoveCmd)

	// issue list
	issueListCmd.Flags().String("output", "table", "Output format: table or json")
	issueListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	issueListCmd.Flags().String("status", "", "Filter by status")
	issueListCmd.Flags().String("priority", "", "Filter by priority")
	issueListCmd.Flags().String("assignee", "", "Filter by assignee name (member, agent, or squad; fuzzy match)")
	issueListCmd.Flags().String("assignee-id", "", "Filter by assignee UUID — member, agent, or squad (mutually exclusive with --assignee)")
	issueListCmd.Flags().String("project", "", "Filter by project ID")
	issueListCmd.Flags().StringSlice("metadata", nil, "Filter by metadata key=value (repeatable; combined with AND). Value is JSON-parsed: 'true'/'false' → bool, numbers → number, otherwise string. Wrap as '\"42\"' to force a string when the value would otherwise sniff as a number.")
	issueListCmd.Flags().StringArray("property", nil, "按属性筛选：Name=Value、Score>=3、Date<2026-01-01、Notes~=text；重复同属性按 OR，不同属性按 AND；含操作符的名字用 UUID")
	issueListCmd.Flags().Int("limit", 50, fmt.Sprintf("Page size, 1 to %d (the server returns at most %d issues per request; use --offset to page through more)", issueListMaxPageSize, issueListMaxPageSize))
	issueListCmd.Flags().Int("offset", 0, "Number of issues to skip (for pagination; while --output json reports has_more, advance it by the number of issues in that same response)")
	issueListCmd.Flags().String("sort", "", "Sort column: position (default, manual board order), title, created_at, start_date, due_date, priority, or property:<name-or-id> to sort by a custom property (select properties sort by option order)")
	issueListCmd.Flags().String("direction", "", "Sort direction (asc or desc); requires --sort to be a non-position column or a property sort (position is always ascending)")
	issueListCmd.Flags().String("fields", "", "JSON output only: comma-separated list of issue fields to include (e.g. id,title,status,priority). Filtering happens client-side after the full response is fetched, so this shrinks CLI output size and agent context cost, not network/server-side cost. Omit for the full issue object (default, unchanged). Valid fields: "+strings.Join(validIssueFields, ", "))
	issueListCmd.Flags().Bool("resolve-properties", false, resolvePropertiesHelp)

	// issue get
	issueGetCmd.Flags().String("output", "json", "Output format: table or json")
	issueGetCmd.Flags().Bool("resolve-properties", false, resolvePropertiesHelp)

	// issue pull-requests
	issuePullRequestsCmd.Flags().String("output", "table", "Output format: table or json")
	issueSourceFetchCmd.Flags().String("provider", "tapd", "Source provider: tapd or gongfeng")
	issueSourceFetchCmd.Flags().String("fetch-provider", "", "Fetch mechanism")
	issueSourceFetchCmd.Flags().String("status", "", "Fetch status: fetched or fetch_failed")
	issueSourceFetchCmd.Flags().String("url", "", "Source URL")
	issueSourceFetchCmd.Flags().String("source-workspace-id", "", "External workspace ID")
	issueSourceFetchCmd.Flags().String("resource-type", "", "External resource type")
	issueSourceFetchCmd.Flags().String("resource-id", "", "External resource ID")
	issueSourceFetchCmd.Flags().String("title", "", "Fetched title")
	issueSourceFetchCmd.Flags().String("summary", "", "Fetched summary")
	issueSourceFetchCmd.Flags().String("body-excerpt", "", "Fetched body excerpt")
	issueSourceFetchCmd.Flags().String("version", "", "External source version")
	issueSourceFetchCmd.Flags().String("error", "", "Fetch failure reason")
	issueSourceFetchCmd.Flags().Int64("duration-ms", 0, "Fetch duration in milliseconds")
	issueSourceFetchCmd.Flags().Bool("auto-fetch", false, "Fetch with the configured account credential")
	issueSourceFetchCmd.Flags().String("output", "json", "Output format: table or json")

	issueChildrenCmd.Flags().String("output", "table", "Output format: table or json")
	issueChildrenCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")

	// issue create
	issueCreateCmd.Flags().String("title", "", "Issue title (required)")
	issueCreateCmd.Flags().String("description", "", "Issue description (decodes \\n, \\r, \\t, \\\\; pipe via --description-stdin to preserve literal backslashes)")
	issueCreateCmd.Flags().Bool("description-stdin", false, "Read issue description from stdin (preserves multi-line content verbatim)")
	issueCreateCmd.Flags().String("description-file", "", "Read issue description from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes). The path must be inside the current working directory unless --allow-external-file is set.")
	issueCreateCmd.Flags().Bool("allow-external-file", false, "Allow --description-file / --attachment to read a path outside the current working directory. Off by default so a stale file from another run/environment can't be picked up (MUL-4252).")
	issueCreateCmd.Flags().String("status", "", "Issue status")
	issueCreateCmd.Flags().String("priority", "", "Issue priority")
	issueCreateCmd.Flags().String("assignee", "", "Assignee name (member, agent, or squad; fuzzy match)")
	issueCreateCmd.Flags().String("assignee-id", "", "Assignee UUID — member, agent, or squad (mutually exclusive with --assignee)")
	issueCreateCmd.Flags().String("parent", "", "Parent issue ID")
	issueCreateCmd.Flags().Int("stage", 0, "Stage ordinal (>=1) grouping this sub-issue into an ordered barrier group under its parent; omit for unstaged. The parent assignee is woken only when every sub-issue in a stage finishes.")
	issueCreateCmd.Flags().String("project", "", "Project ID")
	issueCreateCmd.Flags().String("start-date", "", "Start date (calendar day, YYYY-MM-DD)")
	issueCreateCmd.Flags().String("due-date", "", "Due date (calendar day, YYYY-MM-DD)")
	issueCreateCmd.Flags().Bool("allow-duplicate", false, "Allow creating an issue even when an active duplicate exists")
	issueCreateCmd.Flags().String("output", "json", "Output format: table or json")
	issueCreateCmd.Flags().StringSlice("attachment", nil, "File path(s) to attach (can be specified multiple times). Each file is uploaded and its markdown reference is appended to the description, which is what makes it render on the issue page")
	issueCreateCmd.Flags().StringSlice("attachment-id", nil, "Existing attachment UUID(s) to bind to the created issue (can be specified multiple times)")
	issueCreateCmd.Flags().StringArray("property", nil, `Set a custom property atomically with creation as "Name=Value" (repeatable, one distinct property per flag). Multi-value properties use comma-separated values inside one flag. Property and option/member names are case-insensitive; UUIDs are accepted. Filter-only __none__, >=, <=, and != forms are rejected.`)

	// issue update
	issueUpdateCmd.Flags().String("title", "", "New title")
	issueUpdateCmd.Flags().String("description", "", "New description (decodes \\n, \\r, \\t, \\\\; pipe via --description-stdin to preserve literal backslashes)")
	issueUpdateCmd.Flags().Bool("description-stdin", false, "Read new description from stdin (preserves multi-line content verbatim)")
	issueUpdateCmd.Flags().String("description-file", "", "Read new description from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes). The path must be inside the current working directory unless --allow-external-file is set.")
	issueUpdateCmd.Flags().Bool("allow-external-file", false, "Allow --description-file / --attachment to read a path outside the current working directory. Off by default so a stale temp file from another run/environment can't be picked up (MUL-4252).")
	issueUpdateCmd.Flags().StringSlice("attachment", nil, "Local file path(s) to attach to the issue description (repeatable); references are appended to the end of the description")
	issueUpdateCmd.Flags().String("status", "", "New status")
	issueUpdateCmd.Flags().String("priority", "", "New priority")
	issueUpdateCmd.Flags().String("assignee", "", "New assignee name (member, agent, or squad; fuzzy match)")
	issueUpdateCmd.Flags().String("assignee-id", "", "New assignee UUID — member, agent, or squad (mutually exclusive with --assignee)")
	issueUpdateCmd.Flags().String("project", "", "Project ID")
	issueUpdateCmd.Flags().String("start-date", "", "New start date (calendar day, YYYY-MM-DD; pass empty string to clear)")
	issueUpdateCmd.Flags().String("due-date", "", "New due date (calendar day, YYYY-MM-DD)")
	issueUpdateCmd.Flags().String("parent", "", "Parent issue ID (use --parent \"\" to clear)")
	issueUpdateCmd.Flags().Int("stage", 0, "Stage ordinal (>=1) for this sub-issue; see `issue create --stage`")
	issueUpdateCmd.Flags().Float64("position", 0, "Ordering position within the board column (lower sorts first); prefer `issue reorder` for relative moves")
	issueUpdateCmd.Flags().Bool("no-start", false, "Apply the update without starting an agent run")
	issueUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// issue status
	issueStatusCmd.Flags().Bool("no-start", false, "Change status without starting an agent run")
	issueStatusCmd.Flags().String("output", "table", "Output format: table or json")

	// issue reorder
	registerIssueReorderFlags(issueReorderCmd)

	// issue assign
	issueAssignCmd.Flags().String("to", "", "Assignee name (member, agent, or squad; fuzzy match)")
	issueAssignCmd.Flags().String("to-id", "", "Assignee UUID — member, agent, or squad (mutually exclusive with --to)")
	issueAssignCmd.Flags().Bool("unassign", false, "Remove current assignee")
	issueAssignCmd.Flags().Bool("no-start", false, "Assign ownership without starting an agent run")
	issueAssignCmd.Flags().String("output", "json", "Output format: table or json")

	// issue comment list
	issueCommentListCmd.Flags().String("output", "table", "Output format: table or json")
	issueCommentListCmd.Flags().String("since", "", "Only return comments created after this timestamp (RFC3339)")
	issueCommentListCmd.Flags().String("thread", "", "Comment UUID — return the thread containing this comment (root + every descendant). May be a root or a reply id.")
	issueCommentListCmd.Flags().Int("tail", 0, "Only valid with --thread. Cap reply count to the N most recent replies; the thread root is always included (even with --tail 0). Use --before/--before-id to scroll to older replies.")
	issueCommentListCmd.Flags().Int("recent", 0, "Return the N most recently active threads. N caps THREADS, not comments: every thread carries its root plus EVERY descendant with no per-thread cap, so on an issue with fewer than N root threads this returns the entire history (minus folded resolved threads). Prefer two bounded reads: scan with --roots-only --summary, then open selected threads with --thread <id> --tail N. Use --before/--before-id from the previous response to scroll to older threads.")
	issueCommentListCmd.Flags().Bool("roots-only", false, "Only return top-level comments (parent_id is null). Each root also carries reply_count + last_activity_at so you can triage which thread to open.")
	issueCommentListCmd.Flags().Bool("compact", false, "JSON output only: drop response fields that carry no information for a reader — the issue_id echoed from the request path, source_task_id, updated_at when identical to created_at, null-valued fields, and empty arrays. Content and identity fields pass through untouched. Recommended for agent reads; composes with any mode.")
	issueCommentListCmd.Flags().Bool("summary", false, "Clip each comment's content to a short preview (sets content_truncated) so you can scan a list without pulling full bodies. Composes with any mode.")
	issueCommentListCmd.Flags().Bool("full", false, "Escape hatch: return every comment in resolved threads verbatim. By default the complete-thread reads (default list, --recent, --thread without --tail) are folded — a resolved thread collapses to its root + conclusion, with the dropped count reported on the root — so you do not pay tokens for settled discussion. Pass --full when you need the folded discussion. No effect on --since/--tail/--roots-only reads, which are never folded.")
	issueCommentListCmd.Flags().String("before", "", "Cursor (RFC3339Nano timestamp). With --recent: thread cursor (last_activity_at). With --thread + --tail: reply cursor (reply created_at). Read from the X-Multica-Next-Before response header, printed on stderr as \"Next thread cursor\" / \"Next reply cursor\"; must be paired with --before-id.")
	issueCommentListCmd.Flags().String("before-id", "", "Cursor UUID. With --recent: thread root UUID. With --thread + --tail: oldest reply UUID. Read from the X-Multica-Next-Before-Id response header; must be paired with --before.")

	// issue runs
	issueRunsCmd.Flags().String("output", "table", "Output format: table or json")
	issueRunsCmd.Flags().Bool("full-id", false, "Show full task UUIDs in table output")
	issueRunsCmd.Flags().Bool("active", false, "Only in-flight runs (queued, dispatched, running, waiting_local_directory) instead of the full execution history. Answers \"is an agent working on this right now\" without pulling every past run.")
	issueRunsCmd.Flags().Bool("siblings", false, "Widen to this issue's sub-issue family — its parent (or itself, when it has no parent) plus every child of that parent — so you can see whether another run is already working alongside you before starting overlapping code or PR work. Implies --active. Returns a compact per-run row (task, issue, agent, status, started) rather than the full execution-log record. Ordered running-first, newest-first within a status, and capped at 20 rows; when the cap truncates the answer the CLI says so on stderr, so a short list is never mistaken for a complete one. Advisory only: it reports work in flight, it does not reserve or serialise anything.")

	// issue usage
	issueUsageCmd.Flags().String("output", "table", "Output format: table or json")

	// issue rerun
	issueRerunCmd.Flags().String("output", "json", "Output format: table or json")
	// issue cancel-task
	issueCancelTaskCmd.Flags().String("output", "json", "Output format: table or json")
	issueCancelTaskCmd.Flags().String("issue", "", "Issue ID/key to scope short task ID prefix resolution")
	// issue run-messages
	issueRunMessagesCmd.Flags().String("output", "json", "Output format: table or json")
	issueRunMessagesCmd.Flags().Int("since", 0, "Only return messages after this sequence number")
	issueRunMessagesCmd.Flags().String("message-output", "", "Read a page of complete output by message ID")
	issueRunMessagesCmd.Flags().Int("offset", 0, "Output character offset")
	issueRunMessagesCmd.Flags().String("search", "", "Find text in complete output from offset")
	issueRunMessagesCmd.Flags().String("issue", "", "Issue ID/key to scope short task ID prefix resolution")

	// issue comment add
	issueCommentAddCmd.Flags().String("content", "", "Comment content (decodes \\n, \\r, \\t, \\\\; pipe via --content-stdin for multi-line bodies or to preserve literal backslashes)")
	issueCommentAddCmd.Flags().Bool("content-stdin", false, "Read comment content from stdin (preserves multi-line content verbatim)")
	issueCommentAddCmd.Flags().String("content-file", "", "Read comment content from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes). The path must be inside the current working directory unless --allow-external-file is set.")
	issueCommentAddCmd.Flags().Bool("allow-external-file", false, "Allow --content-file / --attachment to read a path outside the current working directory. Off by default so a stale file from another run/environment can't be picked up (MUL-4252).")
	issueCommentAddCmd.Flags().String("parent", "", "Parent comment ID to reply under. A comment-triggered agent task must reply under its trigger comment; omitting --parent to post a top-level comment is rejected")
	issueCommentAddCmd.Flags().StringSlice("attachment", nil, "File path(s) to attach (can be specified multiple times)")
	issueCommentAddCmd.Flags().String("output", "json", "Output format: table or json")

	// issue comment update
	issueCommentUpdateCmd.Flags().String("content", "", "New comment content (decodes \\n, \\r, \\t, \\\\; pipe via --content-stdin for multi-line bodies or to preserve literal backslashes)")
	issueCommentUpdateCmd.Flags().Bool("content-stdin", false, "Read new comment content from stdin (preserves multi-line content verbatim)")
	issueCommentUpdateCmd.Flags().String("content-file", "", "Read new comment content from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes). The path must be inside the current working directory unless --allow-external-file is set.")
	issueCommentUpdateCmd.Flags().Bool("allow-external-file", false, "Allow --content-file to read a path outside the current working directory. Off by default so a stale file from another run/environment can't be picked up (MUL-4252).")
	issueCommentUpdateCmd.Flags().Int64("expected-revision", 0, "Current positive comment revision from `issue comment list --output json` (required; prevents overwriting a concurrent edit)")
	issueCommentUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// issue comment resolve/unresolve
	issueCommentResolveCmd.Flags().String("output", "json", "Output format: table or json")
	issueCommentUnresolveCmd.Flags().String("output", "json", "Output format: table or json")

	// issue search
	issueSearchCmd.Flags().Int("limit", 20, "Maximum number of results to return")
	issueSearchCmd.Flags().Bool("include-closed", false, "Include done and cancelled issues")
	issueSearchCmd.Flags().String("output", "table", "Output format: table or json")

	// issue subscriber list
	issueSubscriberListCmd.Flags().String("output", "table", "Output format: table or json")

	// issue subscriber add
	issueSubscriberAddCmd.Flags().String("user", "", "Member or agent name to subscribe (fuzzy match; defaults to the caller)")
	issueSubscriberAddCmd.Flags().String("user-id", "", "Member or agent UUID to subscribe (mutually exclusive with --user)")
	issueSubscriberAddCmd.Flags().String("output", "json", "Output format: table or json")

	// issue subscriber remove
	issueSubscriberRemoveCmd.Flags().String("user", "", "Member or agent name to unsubscribe (fuzzy match; defaults to the caller)")
	issueSubscriberRemoveCmd.Flags().String("user-id", "", "Member or agent UUID to unsubscribe (mutually exclusive with --user)")
	issueSubscriberRemoveCmd.Flags().String("output", "json", "Output format: table or json")
}

// ---------------------------------------------------------------------------
// Issue commands —— 各 issue 子命令的 RunE 实现
// ---------------------------------------------------------------------------

// runIssueList 执行 `issue list`：把状态/优先级/assignee/project/metadata/分页/排序
// 等过滤条件拼成查询参数后 GET /api/issues，再按 --output 打表格或 JSON。表格输出会
// 先加载 actor 查找表，把 assignee 的 (type, uuid) 渲染成可读名字。
func runIssueList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if client.WorkspaceID == "" {
		if _, err := requireWorkspaceID(cmd); err != nil {
			return err
		}
	}

	// The server clamps limit and applies its own default when the flag is
	// missing, so reject a value it cannot honour up front (before any
	// request) rather than report a page that was never returned. Same
	// reasoning as the --direction guard below.
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > issueListMaxPageSize {
		return fmt.Errorf("--limit must be between 1 and %d (the server returns at most %d issues per request); use --offset to page through more", issueListMaxPageSize, issueListMaxPageSize)
	}
	offset, _ := cmd.Flags().GetInt("offset")
	if offset < 0 {
		return fmt.Errorf("--offset must be zero or greater")
	}

	params := url.Values{}
	params.Set("workspace_id", client.WorkspaceID)
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		params.Set("status", v)
	}
	if v, _ := cmd.Flags().GetString("priority"); v != "" {
		params.Set("priority", v)
	}
	params.Set("limit", fmt.Sprintf("%d", limit))
	if offset > 0 {
		params.Set("offset", fmt.Sprintf("%d", offset))
	}
	_, aID, hasAssignee, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "assignee", "assignee-id", issueAssigneeKinds)
	if resolveErr != nil {
		return fmt.Errorf("resolve assignee: %w", resolveErr)
	}
	if hasAssignee {
		params.Set("assignee_id", aID)
	}
	if v, _ := cmd.Flags().GetString("project"); v != "" {
		project, err := resolveProjectID(ctx, client, v)
		if err != nil {
			return err
		}
		params.Set("project_id", project.ID)
	}
	if mdFlags, _ := cmd.Flags().GetStringSlice("metadata"); len(mdFlags) > 0 {
		filter, err := buildMetadataFilterQueryParam(mdFlags)
		if err != nil {
			return err
		}
		params.Set("metadata", filter)
	}
	// --property filtering and property:<ref> sorting both address definitions
	// by name or UUID, so they share a single catalog fetch. An actor filter
	// and --resolve-properties share one member request the same way.
	const propertySortPrefix = "property:"
	propertyFlags, _ := cmd.Flags().GetStringArray("property")
	sortVal, _ := cmd.Flags().GetString("sort")
	var properties []propertyDTO
	var members memberDirectory
	if len(propertyFlags) > 0 || strings.HasPrefix(sortVal, propertySortPrefix) {
		var err error
		if properties, err = fetchProperties(ctx, client); err != nil {
			return err
		}
	}
	if len(propertyFlags) > 0 {
		filter, err := buildPropertiesFilterQueryParam(ctx, client, &members, properties, propertyFlags)
		if err != nil {
			return err
		}
		params.Set("properties", filter)
	}
	if strings.HasPrefix(sortVal, propertySortPrefix) {
		property, err := resolveSortableProperty(properties, strings.TrimPrefix(sortVal, propertySortPrefix))
		if err != nil {
			return err
		}
		params.Set("sort", propertySortPrefix+property.ID)
	} else if sortVal != "" {
		valid := false
		for _, c := range validIssueSortColumns {
			if c == sortVal {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid --sort %q; valid values: %s, or property:<name-or-id> for a custom property", sortVal, strings.Join(validIssueSortColumns, ", "))
		}
		params.Set("sort", sortVal)
	}
	if v, _ := cmd.Flags().GetString("direction"); v != "" {
		d := strings.ToLower(v)
		if d != "asc" && d != "desc" {
			return fmt.Errorf("invalid --direction %q; valid values: asc, desc", v)
		}
		// position（手动看板顺序）永远升序，server 会忽略针对它的 --direction。
		// 与其静默吞掉这个「传了但没用」的 flag（对脚本是隐患），不如在本地先拒绝该组合。
		if sortVal == "" || sortVal == "position" {
			return fmt.Errorf("--direction requires --sort to be one of %s, or a property:<name-or-id> sort; position (the default manual board order) is always ascending", strings.Join(directionalIssueSortColumns, ", "))
		}
		params.Set("direction", d)
	}

	var fields []string
	if v, _ := cmd.Flags().GetString("fields"); v != "" {
		valid := make(map[string]bool, len(validIssueFields))
		for _, f := range validIssueFields {
			valid[f] = true
		}
		keepsProperties := false
		for _, f := range strings.Split(v, ",") {
			f = strings.TrimSpace(f)
			if !valid[f] {
				return fmt.Errorf("invalid --fields value %q; valid values: %s", f, strings.Join(validIssueFields, ", "))
			}
			if f == "properties" {
				keepsProperties = true
			}
			fields = append(fields, f)
		}
		// --fields without `properties` deletes the very key
		// --resolve-properties rewrites, so the pair would either cost two
		// requests for output nobody sees or leave the flag silently doing
		// nothing. A passed-but-ignored flag is a footgun in scripts (the
		// same reason --fields rejects "assignee" instead of dropping it),
		// so say so instead of picking one of those.
		//
		// Only in JSON mode, though: both flags document themselves as having
		// no effect on --output table, so a table reader who leaves them on
		// the command line must still get their table.
		outputFormat, _ := cmd.Flags().GetString("output")
		resolve, _ := cmd.Flags().GetBool("resolve-properties")
		if outputFormat == "json" && resolve && !keepsProperties {
			return fmt.Errorf("--resolve-properties needs the properties field, but --fields does not include it; add properties to --fields or drop --resolve-properties")
		}
	}

	path := "/api/issues"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("list issues: %w", err)
	}

	issuesRaw, _ := result["issues"].([]any)
	total, totalOK := result["total"].(float64)
	returned := len(issuesRaw)
	// total cannot end a walk on its own. The server answers with the row
	// count it just returned when its count query fails, and a newer backend
	// may drop the field; either one reports has_more false beside a full page
	// and truncates the walk in silence. So total is trusted only when it is
	// larger than this page, which a failed count never is; otherwise the page
	// decides. The one cost is a first page that fills exactly: it cannot tell
	// a genuine total from a failed count, so it probes onward and the next
	// request comes back empty.
	totalTrusted := totalOK && int(total) > returned
	hasMore := false
	switch {
	case returned == 0:
		// An empty page ends a walk whatever total says.
	case totalTrusted:
		hasMore = offset+returned < int(total)
	default:
		hasMore = returned == limit
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		// --fields runs first so a page that drops `properties` never pays for
		// the catalog and member requests resolving it would need. Combining
		// the two with `properties` filtered out is rejected above, so nothing
		// resolvable is deleted before it is resolved.
		if len(fields) > 0 {
			filterIssueFields(issuesRaw, fields)
		}
		if resolve, _ := cmd.Flags().GetBool("resolve-properties"); resolve {
			if err := resolveIssueProperties(ctx, client, properties, &members, issuesRaw); err != nil {
				return err
			}
		}
		wrapped := map[string]any{
			"issues":   issuesRaw,
			"total":    int(total),
			"limit":    limit,
			"offset":   offset,
			"has_more": hasMore,
		}
		return cli.PrintJSON(os.Stdout, wrapped)
	}

	fullID, _ := cmd.Flags().GetBool("full-id")
	headers := []string{"KEY", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "START DATE", "DUE DATE"}
	if fullID {
		headers = []string{"KEY", "ID", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "START DATE", "DUE DATE"}
	}
	actors := loadActorDisplayLookup(ctx, client)
	rows := make([][]string, 0, len(issuesRaw))
	for _, raw := range issuesRaw {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		assignee := formatAssignee(issue, actors)
		startDate := strVal(issue, "start_date")
		if startDate != "" && len(startDate) >= 10 {
			startDate = startDate[:10]
		}
		dueDate := strVal(issue, "due_date")
		if dueDate != "" && len(dueDate) >= 10 {
			dueDate = dueDate[:10]
		}
		row := []string{
			issueDisplayKey(issue),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
			assignee,
			startDate,
			dueDate,
		}
		if fullID {
			row = []string{
				issueDisplayKey(issue),
				strVal(issue, "id"),
				strVal(issue, "title"),
				strVal(issue, "status"),
				strVal(issue, "priority"),
				assignee,
				startDate,
				dueDate,
			}
		}
		rows = append(rows, row)
	}
	cli.PrintTable(os.Stdout, headers, rows)
	// Page footer on stderr so stdout stays a plain table. It speaks whenever
	// there is more or the page was offset, and stays silent for a short
	// first page. "of N" appears only while total is trusted, so the line
	// never claims a page is the whole list beside a pointer to the next one.
	switch {
	case returned == 0 && offset > 0:
		if totalTrusted {
			fmt.Fprintf(os.Stderr, "No issues at --offset %d (%d total).\n", offset, int(total))
		} else {
			fmt.Fprintf(os.Stderr, "No issues at --offset %d.\n", offset)
		}
	case hasMore || offset > 0:
		line := fmt.Sprintf("Showing issues %d-%d.", offset+1, offset+returned)
		if totalTrusted {
			line = fmt.Sprintf("Showing %d-%d of %d issues.", offset+1, offset+returned, int(total))
		}
		if hasMore {
			line += fmt.Sprintf(" Next page: --offset %d", offset+returned)
		}
		fmt.Fprintln(os.Stderr, line)
	}
	return nil
}

// runIssuePullRequests 执行 `issue pull-requests <id>`（别名 prs）：拉取关联到该
// issue 的 PR 列表，再按 --output 打表格或 JSON。
func runIssuePullRequests(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/pull-requests", &result); err != nil {
		return fmt.Errorf("list issue pull requests: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	prs, _ := result["pull_requests"].([]any)
	printIssuePullRequestsTable(normalizePullRequestList(prs))
	return nil
}

func normalizePullRequestList(raw []any) []map[string]any {
	prs := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		pr, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prs = append(prs, pr)
	}
	return prs
}

func printIssuePullRequestsTable(prs []map[string]any) {
	headers := []string{"NUMBER", "STATE", "TITLE", "URL"}
	rows := make([][]string, 0, len(prs))
	for _, pr := range prs {
		rows = append(rows, []string{
			strVal(pr, "number"),
			strVal(pr, "state"),
			strVal(pr, "title"),
			pullRequestURL(pr),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
}

func pullRequestURL(pr map[string]any) string {
	if url := strVal(pr, "url"); url != "" {
		return url
	}
	return strVal(pr, "html_url")
}

// runIssueGet 执行 `issue get <id>`：按 id/key 取单个 issue 的完整详情；表格输出
// 会把 assignee 渲染成可读名字、把日期裁到 YYYY-MM-DD，其余场景直接输出 JSON。
func runIssueGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var issue map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID, &issue); err != nil {
		return fmt.Errorf("get issue: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		actors := loadActorDisplayLookup(ctx, client)
		assignee := formatAssignee(issue, actors)
		startDate := strVal(issue, "start_date")
		if startDate != "" && len(startDate) >= 10 {
			startDate = startDate[:10]
		}
		dueDate := strVal(issue, "due_date")
		if dueDate != "" && len(dueDate) >= 10 {
			dueDate = dueDate[:10]
		}
		headers := []string{"KEY", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "START DATE", "DUE DATE", "DESCRIPTION"}
		rows := [][]string{{
			issueDisplayKey(issue),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
			assignee,
			startDate,
			dueDate,
			strVal(issue, "description"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	if resolve, _ := cmd.Flags().GetBool("resolve-properties"); resolve {
		if err := resolveIssueProperties(ctx, client, nil, &memberDirectory{}, []any{issue}); err != nil {
			return err
		}
	}
	return cli.PrintJSON(os.Stdout, issue)
}

// childStage 从子 issue 的响应 map 里取出整数形式的 stage。
// stage 字段为 null/缺失（即未分阶段）时返回 ok=false。
func childStage(m map[string]any) (int, bool) {
	v, ok := m["stage"]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

// runIssueChildren 执行 `issue children <id>`（别名 subissues）：拉取子 issue 列表；
// 表格输出按 stage 升序（未分阶段排最后），JSON 输出按 stage 分组并附每组完成计数，
// 让 agent 一眼掌握阶段进度。
func runIssueChildren(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var resp struct {
		Issues []map[string]any `json:"issues"`
	}
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/children", &resp); err != nil {
		return fmt.Errorf("list child issues: %w", err)
	}
	children := resp.Issues

	// 排序：stage 升序、未分阶段（unstaged）排最后；
	// 同 stage 内部保持 API 返回的顺序（position，再按 created_at desc）。
	sort.SliceStable(children, func(i, j int) bool {
		si, oki := childStage(children[i])
		sj, okj := childStage(children[j])
		if oki != okj {
			return oki // 已分阶段（staged）排在未分阶段（unstaged）前
		}
		return si < sj
	})

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		actors := loadActorDisplayLookup(ctx, client)
		headers := []string{"STAGE", "KEY", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE"}
		rows := make([][]string, 0, len(children))
		for _, c := range children {
			stageCell := "-"
			if s, ok := childStage(c); ok {
				stageCell = strconv.Itoa(s)
			}
			rows = append(rows, []string{
				stageCell,
				issueDisplayKey(c),
				strVal(c, "title"),
				strVal(c, "status"),
				strVal(c, "priority"),
				formatAssignee(c, actors),
			})
		}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	// JSON：按 stage 分组输出，让 agent 一眼看清共有多少子 issue、
	// 分属哪个 stage（各组还带 done 完成数统计）。
	type stageGroup struct {
		Stage  int              `json:"stage"`
		Total  int              `json:"total"`
		Done   int              `json:"done"`
		Issues []map[string]any `json:"issues"`
	}
	stages := []stageGroup{}
	unstaged := []map[string]any{}
	idxByStage := map[int]int{}
	for _, c := range children {
		s, ok := childStage(c)
		if !ok {
			unstaged = append(unstaged, c)
			continue
		}
		gi, seen := idxByStage[s]
		if !seen {
			stages = append(stages, stageGroup{Stage: s})
			gi = len(stages) - 1
			idxByStage[s] = gi
		}
		stages[gi].Issues = append(stages[gi].Issues, c)
		stages[gi].Total++
		if isTerminalChildIssue(c) {
			stages[gi].Done++
		}
	}
	return cli.PrintJSON(os.Stdout, map[string]any{
		"total":    len(children),
		"stages":   stages,
		"unstaged": unstaged,
	})
}

// isTerminalChildIssue 判断一个子 issue 是否算「已完成」，用于 stage 进度统计。
//
// 优先用 `status_category`——它由 server 针对自定义状态解析出来：工作区可自定义
// 状态，只要某个自定义状态落在 `done` 类别里，这里就必须把它算作完成，否则 agent
// 会读到错误的进度。字段缺失时回退到原始 status 字段，使旧版后端的内置状态仍能
// 正确统计。(MUL-6243)
func isTerminalChildIssue(c map[string]any) bool {
	status := strVal(c, "status_category")
	if status == "" {
		status = strVal(c, "status")
	}
	return status == "done" || status == "cancelled"
}

// isHTTPURL 判断 path 是否是 http:// 或 https:// 开头的 URL。
// 用于把传给 --attachment 的 URL 形值滤掉——该参数只接受本地文件路径。
// 先去掉两端空白再判断，因为 agent 生成的命令偶尔会带着多余空格复制 URL。
func isHTTPURL(path string) bool {
	p := strings.TrimSpace(path)
	return strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://")
}

// ensureAttachmentWithinWorkdir 对本地 --attachment 路径施加与 --description-file /
// --content-file 相同的工作目录守卫（MUL-4252）。若 agent 把图表/报告写到 /tmp 这类
// 机器共享路径再作为附件上传，可能误取到另一份运行——甚至另一工作区——留下的陈旧
// 文件（即 /tmp/desc.md 泄漏的附件版）。URL 形值由调用方先行滤除，不会走到这里。
// --allow-external-file 可覆盖，与文本 flag 的逃生门保持一致。
func ensureAttachmentWithinWorkdir(cmd *cobra.Command, filePath string) error {
	if allow, _ := cmd.Flags().GetBool("allow-external-file"); allow {
		return nil
	}
	within, err := fileWithinWorkingDir(filePath)
	if err != nil {
		return fmt.Errorf("resolve --attachment path %q: %w", filePath, err)
	}
	if !within {
		if !pathExists(filePath) {
			return fmt.Errorf(
				"--attachment path %q does not exist; it also resolves outside the current working "+
					"directory, so --allow-external-file would not make this upload succeed. Generate the "+
					"file inside the task workdir and attach that path.",
				filePath)
		}
		return fmt.Errorf(
			"--attachment path %q resolves outside the current working directory; "+
				"attach files generated inside the task workdir rather than machine-shared "+
				"paths like /tmp, where another run's stale file can be attached by mistake. "+
				"Pass --allow-external-file to override.",
			filePath)
	}
	return nil
}

// pendingAttachment 是一个通过了 URL 过滤和工作目录守卫、已被读入内存、等待上传的
// 本地 --attachment 文件。
type pendingAttachment struct {
	path string
	data []byte
}

// collectLocalAttachments 在任何上传发生之前，一次性校验并读取所有本地
// --attachment 路径。URL 形值给出警告后跳过（该 API 只接受本地路径）；其余每个
// 路径都要过 MUL-4252 的工作目录守卫并读入内存，任一路径非法或不可读都会报错且
// 什么都还没上传。`issue create` 与 `comment add` 共用此函数，保证「附件非法」时
// 不会出现前几个附件已上传成孤儿、issue/comment 却没建成的半成品状态（否则重试会
// 产生重复）。
func collectLocalAttachments(cmd *cobra.Command, attachments []string) ([]pendingAttachment, error) {
	pending := make([]pendingAttachment, 0, len(attachments))
	for _, filePath := range attachments {
		if isHTTPURL(filePath) {
			fmt.Fprintf(os.Stderr, "Skipping --attachment %q: URLs are not supported here, only local file paths.\n", filePath)
			continue
		}
		if err := ensureAttachmentWithinWorkdir(cmd, filePath); err != nil {
			return nil, err
		}
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return nil, fmt.Errorf("read attachment %s: %w", filePath, readErr)
		}
		pending = append(pending, pendingAttachment{path: filePath, data: data})
	}
	return pending, nil
}

// appendAttachmentReferences appends the markdown snippet of every uploaded
// attachment to an issue description, so the file renders on the issue page
// instead of only existing as a stored row. Each snippet goes in its own
// paragraph because file cards are block-level. An attachment the description
// already references is skipped — a caller may have composed the markdown
// itself (quick-create keeps the user's pasted image inline), and appending it
// again would render the same file twice.
func appendAttachmentReferences(description string, attachments []cli.AttachmentResponse) string {
	snippets := make([]string, 0, len(attachments))
	for _, att := range attachments {
		if descriptionReferencesAttachment(description, att) {
			continue
		}
		snippets = append(snippets, attachmentMarkdown(filepath.Base(att.Filename), att.ContentType, att.MarkdownURL))
	}
	if len(snippets) == 0 {
		return description
	}
	appended := strings.Join(snippets, "\n\n")
	if strings.TrimSpace(description) == "" {
		return appended
	}
	return strings.TrimRight(description, "\n") + "\n\n" + appended
}

// descriptionReferencesAttachment reports whether a description body already
// points at this attachment. It matches the durable `markdown_url` and the
// `/api/attachments/<id>` path it is built from rather than the raw storage
// `url`, which bodies never carry — the same rule the web composers use
// (`contentReferencesAttachment`).
func descriptionReferencesAttachment(description string, att cli.AttachmentResponse) bool {
	if description == "" {
		return false
	}
	if att.MarkdownURL != "" && strings.Contains(description, att.MarkdownURL) {
		return true
	}
	if att.ID != "" && strings.Contains(description, "/api/attachments/"+att.ID) {
		return true
	}
	return false
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	out := make([]string, 0, len(dst)+len(values))
	for _, v := range append(dst, values...) {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// quickCreateAttachmentIDsFromEnv 从环境变量 MULTICA_QUICK_CREATE_ATTACHMENT_IDS
// 读取一个 JSON 字符串数组（daemon 发起 quick-create 时注入），返回去重后的附件 ID
// 列表；变量为空时返回 nil。
func quickCreateAttachmentIDsFromEnv() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv("MULTICA_QUICK_CREATE_ATTACHMENT_IDS"))
	if raw == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("parse MULTICA_QUICK_CREATE_ATTACHMENT_IDS: %w", err)
	}
	return appendUniqueStrings(nil, ids...), nil
}

// runIssueCreate 执行 `issue create`：收集标题/描述/状态等字段并做本地校验，构造
// 请求体（含 quick-create 来源标记与父/子 issue、附件），先校验全部本地附件再 POST
// 创建；创建成功后补传附件（此处失败只警告不报错，避免重试造成重复）。
func runIssueCreate(cmd *cobra.Command, _ []string) error {
	title, _ := cmd.Flags().GetString("title")
	if title == "" {
		return fmt.Errorf("--title is required")
	}
	statusFlag, _ := cmd.Flags().GetString("status")
	if statusFlag != "" {
		if err := validateIssueStatus(statusFlag); err != nil {
			return err
		}
	}
	priorityFlag, _ := cmd.Flags().GetString("priority")
	if priorityFlag != "" {
		if err := validateIssuePriority(priorityFlag); err != nil {
			return err
		}
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	// 带了附件时放宽超时——文件上传可能很慢。
	timeout := cli.APITimeout()
	attachments, _ := cmd.Flags().GetStringSlice("attachment")
	if len(attachments) > 0 {
		timeout = cli.AtLeastAPITimeout(60 * time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	body := map[string]any{"title": title}
	propertyFlags, _ := cmd.Flags().GetStringArray("property")
	var createProperties map[string]json.RawMessage
	if len(propertyFlags) > 0 {
		var config struct {
			IssueCreatePropertiesSupported bool `json:"issue_create_properties_supported"`
		}
		if err := client.GetJSON(ctx, "/api/config", &config); err != nil {
			return fmt.Errorf("check issue-create property support: %w", err)
		}
		if !config.IssueCreatePropertiesSupported {
			return errors.New("this server version does not support atomic custom properties on issue creation; update the server before using --property")
		}
		createProperties, err = buildIssueCreateProperties(ctx, client, propertyFlags)
		if err != nil {
			return err
		}
		body["properties"] = createProperties
	}
	desc, hasDesc, err := resolveTextFlag(cmd, "description")
	if err != nil {
		return err
	}
	if hasDesc {
		if err := guardLocalPathLinks(desc, "issue description",
			"Deliver the file itself with `multica issue create --attachment <path>` (repeatable) and drop the link."); err != nil {
			return err
		}
	}
	if statusFlag != "" {
		body["status"] = statusFlag
	}
	if priorityFlag != "" {
		body["priority"] = priorityFlag
	}
	if v, _ := cmd.Flags().GetString("parent"); v != "" {
		parent, err := resolveIssueRef(ctx, client, v)
		if err != nil {
			return fmt.Errorf("resolve parent issue: %w", err)
		}
		body["parent_issue_id"] = parent.ID
	}
	if v, _ := cmd.Flags().GetString("project"); v != "" {
		project, err := resolveProjectID(ctx, client, v)
		if err != nil {
			return fmt.Errorf("resolve project: %w", err)
		}
		body["project_id"] = project.ID
	}
	if cmd.Flags().Changed("stage") {
		stage, _ := cmd.Flags().GetInt("stage")
		if stage < 1 {
			return fmt.Errorf("--stage must be >= 1")
		}
		body["stage"] = stage
	}
	if v, _ := cmd.Flags().GetString("start-date"); v != "" {
		body["start_date"] = v
	}
	if v, _ := cmd.Flags().GetString("due-date"); v != "" {
		body["due_date"] = v
	}
	if v, _ := cmd.Flags().GetBool("allow-duplicate"); v {
		body["allow_duplicate"] = true
	}
	aType, aID, hasAssignee, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "assignee", "assignee-id", issueAssigneeKinds)
	if resolveErr != nil {
		return fmt.Errorf("resolve assignee: %w", resolveErr)
	}
	if hasAssignee {
		body["assignee_type"] = aType
		body["assignee_id"] = aID
	}

	// Quick-create 标记：daemon 在唤起 agent 前会设置 MULTICA_QUICK_CREATE_TASK_ID，
	// agent 里的 `multica issue create` 调用便继承了该环境变量，把新 issue 打上
	// origin_type=quick_create + origin_id=<task_id> 的标记。随后完成处理器靠 origin
	// 确定性定位这个 issue，而不是用「该 agent 最近创建的 issue」——后者在
	// max_concurrent_tasks > 1 且 agent 并行建别的 issue 时会有竞态。
	if taskID := os.Getenv("MULTICA_QUICK_CREATE_TASK_ID"); taskID != "" {
		body["origin_type"] = "quick_create"
		body["origin_id"] = taskID
	}
	attachmentIDs, _ := cmd.Flags().GetStringSlice("attachment-id")
	envAttachmentIDs, err := quickCreateAttachmentIDsFromEnv()
	if err != nil {
		return err
	}
	attachmentIDs = appendUniqueStrings(attachmentIDs, envAttachmentIDs...)

	// 在创建 issue 之前先预校验附件：坏的路径绝不能产生一个建了一半的 issue
	// （否则调用方——尤其是正在 quick-create 的 agent——会整体重试 `issue create`，
	// 最终得到一堆重复）。URL 形值警告后跳过、过工作目录守卫、所有本地路径提前
	// 读入内存；这里任一失败都在创建前暴露，issue 根本不会落库。
	pending, err := collectLocalAttachments(cmd, attachments)
	if err != nil {
		return err
	}

	// Upload BEFORE creating the issue, and append each file's markdown to the
	// description. A file is visible on an issue only when the description
	// references it — every other writer honors that (the web create dialog and
	// the description editor bind exactly what the body references). Uploading
	// after the create, as this used to, stored the file with an `issue_id` and
	// left the description untouched, so it rendered nowhere on web, desktop or
	// mobile (MUL-7600 / #8692). Uploading first also removes the old
	// partial-success state: a failure here means no issue was created, so the
	// retry is safe and cannot duplicate.
	uploaded := make([]cli.AttachmentResponse, 0, len(pending))
	for _, att := range pending {
		result, uploadErr := client.UploadIssueAttachment(ctx, att.data, att.path, "")
		if uploadErr != nil {
			return fmt.Errorf("upload attachment %s (no issue created): %w", att.path, uploadErr)
		}
		uploaded = append(uploaded, result)
		attachmentIDs = appendUniqueStrings(attachmentIDs, result.ID)
		fmt.Fprintf(os.Stderr, "Uploaded %s\n", att.path)
	}
	desc = appendAttachmentReferences(desc, uploaded)
	if hasDesc || len(uploaded) > 0 {
		body["description"] = desc
	}
	if len(attachmentIDs) > 0 {
		body["attachment_ids"] = attachmentIDs
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues", body, &result); err != nil {
		if msg, ok := activeDuplicateIssueCreateMessage(err); ok {
			return errors.New(msg)
		}
		return fmt.Errorf("create issue: %w", err)
	}
	if err := verifyIssueCreateProperties(createProperties, result); err != nil {
		return fmt.Errorf("issue %s was created, but the server did not confirm its custom properties; review it before retrying: %w", issueDisplayKey(result), err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"KEY", "TITLE", "STATUS", "PRIORITY"}
		rows := [][]string{{
			issueDisplayKey(result),
			strVal(result, "title"),
			strVal(result, "status"),
			strVal(result, "priority"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, result)
}

func verifyIssueCreateProperties(expected map[string]json.RawMessage, issue map[string]any) error {
	if len(expected) == 0 {
		return nil
	}
	bag, ok := issue["properties"].(map[string]any)
	if !ok {
		return errors.New("response omitted the properties snapshot")
	}
	for propertyID, encoded := range expected {
		actual, exists := bag[propertyID]
		if !exists {
			return fmt.Errorf("response omitted property %s", propertyID)
		}
		var want any
		if err := json.Unmarshal(encoded, &want); err != nil {
			return fmt.Errorf("decode expected property %s: %w", propertyID, err)
		}
		if !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("response property %s does not match the canonical value", propertyID)
		}
	}
	return nil
}

func activeDuplicateIssueCreateMessage(err error) (string, bool) {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		return "", false
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &payload) != nil {
		return "", false
	}
	if payload.Code != "active_duplicate_issue" || payload.Error == "" {
		return "", false
	}
	return payload.Error, true
}

// runIssueUpdate 执行 `issue update <id>`：把通过 flag 显式改动的字段（title/
// description/status/priority/assignee/project/日期/parent/stage/position）收集成
// 请求体。description 只读取一次——三个来源 flag 都传时由 resolveTextFlag 报互斥错。
func runIssueUpdate(cmd *cobra.Command, args []string) error {
	attachmentPaths, _ := cmd.Flags().GetStringSlice("attachment")
	noStart, _ := cmd.Flags().GetBool("no-start")
	statusChanged := cmd.Flags().Changed("status")
	statusFlag, _ := cmd.Flags().GetString("status")
	if statusChanged {
		if err := validateIssueStatus(statusFlag); err != nil {
			return err
		}
	}
	priorityChanged := cmd.Flags().Changed("priority")
	priorityFlag, _ := cmd.Flags().GetString("priority")
	if priorityChanged {
		if err := validateIssuePriority(priorityFlag); err != nil {
			return err
		}
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	timeout := cli.APITimeout()
	if len(attachmentPaths) > 0 {
		timeout = cli.AtLeastAPITimeout(60 * time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{}
	if cmd.Flags().Changed("title") {
		v, _ := cmd.Flags().GetString("title")
		body["title"] = v
	}
	if cmd.Flags().Changed("description") || cmd.Flags().Changed("description-stdin") || cmd.Flags().Changed("description-file") {
		desc, _, err := resolveTextFlag(cmd, "description")
		if err != nil {
			return err
		}
		// `issue update` 没有 --attachment 参数，所以这里的提示必须指向带该参数的命令。
		// 如果只让 agent「传 --attachment」，指的就是本命令会拒绝的参数名。
		if err := guardLocalPathLinks(desc, "issue description",
			"Attach the file with `multica issue update <issue-id> --attachment <path>` and drop the local-path link."); err != nil {
			return err
		}
		body["description"] = desc
	}
	if statusChanged {
		body["status"] = statusFlag
	}
	if priorityChanged {
		body["priority"] = priorityFlag
	}
	if cmd.Flags().Changed("project") {
		v, _ := cmd.Flags().GetString("project")
		if v == "" {
			body["project_id"] = nil
		} else {
			project, err := resolveProjectID(ctx, client, v)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			body["project_id"] = project.ID
		}
	}
	if cmd.Flags().Changed("start-date") {
		v, _ := cmd.Flags().GetString("start-date")
		body["start_date"] = v
	}
	if cmd.Flags().Changed("due-date") {
		v, _ := cmd.Flags().GetString("due-date")
		body["due_date"] = v
	}
	if cmd.Flags().Changed("assignee") || cmd.Flags().Changed("assignee-id") {
		aType, aID, hasAssignee, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "assignee", "assignee-id", issueAssigneeKinds)
		if resolveErr != nil {
			return fmt.Errorf("resolve assignee: %w", resolveErr)
		}
		if hasAssignee {
			body["assignee_type"] = aType
			body["assignee_id"] = aID
		}
	}
	if cmd.Flags().Changed("parent") {
		v, _ := cmd.Flags().GetString("parent")
		if v == "" {
			body["parent_issue_id"] = nil
		} else {
			parent, err := resolveIssueRef(ctx, client, v)
			if err != nil {
				return fmt.Errorf("resolve parent issue: %w", err)
			}
			body["parent_issue_id"] = parent.ID
		}
	}
	if cmd.Flags().Changed("stage") {
		stage, _ := cmd.Flags().GetInt("stage")
		if stage < 1 {
			return fmt.Errorf("--stage must be >= 1")
		}
		body["stage"] = stage
	}
	if cmd.Flags().Changed("position") {
		v, _ := cmd.Flags().GetFloat64("position")
		body["position"] = v
	}

	if len(body) == 0 && len(attachmentPaths) == 0 {
		return fmt.Errorf("no fields to update; use flags like --title, --status, --priority, --assignee, etc.")
	}
	// Validate every path before any upload so an invalid later path cannot
	// leave earlier files uploaded and unbound.
	pending, err := collectLocalAttachments(cmd, attachmentPaths)
	if err != nil {
		return err
	}
	if len(body) == 0 && len(pending) == 0 {
		return fmt.Errorf("no local attachments to update; --attachment accepts file paths, not URLs")
	}
	desc, changed := body["description"].(string)
	if len(pending) > 0 && !changed {
		var issue struct {
			Description *string `json:"description"`
		}
		if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID), &issue); err != nil {
			return fmt.Errorf("get issue description before binding attachments: %w", err)
		}
		if issue.Description != nil {
			desc = *issue.Description
		}
	}
	attachmentIDs := make([]string, 0, len(pending))
	attachmentRefs := make([]cli.AttachmentResponse, 0, len(pending))
	for _, att := range pending {
		uploaded, uploadErr := client.UploadIssueAttachment(ctx, att.data, att.path, "")
		if uploadErr != nil {
			return fmt.Errorf("upload attachment %s: %w; already uploaded IDs: %v", att.path, uploadErr, attachmentIDs)
		}
		attachmentIDs = append(attachmentIDs, uploaded.ID)
		attachmentRefs = append(attachmentRefs, uploaded)
		fmt.Fprintf(os.Stderr, "Uploaded %s\n", att.path)
	}
	if len(attachmentRefs) > 0 {
		body["description"] = appendAttachmentReferences(desc, attachmentRefs)
		body["attachment_ids"] = attachmentIDs
	}
	if noStart {
		body["suppress_run"] = true
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+issueRef.ID, body, &result); err != nil {
		if len(attachmentIDs) > 0 {
			return fmt.Errorf("update issue (uploaded IDs: %v): %w", attachmentIDs, err)
		}
		return fmt.Errorf("update issue: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"KEY", "TITLE", "STATUS", "PRIORITY"}
		rows := [][]string{{
			issueDisplayKey(result),
			strVal(result, "title"),
			strVal(result, "status"),
			strVal(result, "priority"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, result)
}

// runIssueAssign 执行 `issue assign <id>`：把 issue 指派给某个 member/agent/squad
// （--to/--to-id），或 --unassign 清空指派。变更会触发一次 agent run，--no-start
// 可只改归属不起任务。
func runIssueAssign(cmd *cobra.Command, args []string) error {
	toName, _ := cmd.Flags().GetString("to")
	unassign, _ := cmd.Flags().GetBool("unassign")
	noStart, _ := cmd.Flags().GetBool("no-start")
	toNameSet := cmd.Flags().Changed("to")
	toIDSet := cmd.Flags().Changed("to-id")

	if !toNameSet && !toIDSet && !unassign {
		return fmt.Errorf("provide --to <name>, --to-id <uuid>, or --unassign")
	}
	if (toNameSet || toIDSet) && unassign {
		return fmt.Errorf("--to/--to-id and --unassign are mutually exclusive")
	}
	if noStart && unassign {
		return fmt.Errorf("--no-start cannot be used with --unassign")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{}
	displayTarget := toName
	if unassign {
		body["assignee_type"] = nil
		body["assignee_id"] = nil
	} else {
		aType, aID, _, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "to", "to-id", issueAssigneeKinds)
		if resolveErr != nil {
			return fmt.Errorf("resolve assignee: %w", resolveErr)
		}
		body["assignee_type"] = aType
		body["assignee_id"] = aID
		if displayTarget == "" {
			displayTarget = loadActorDisplayLookup(ctx, client).actor(aType, aID)
		}
		if noStart {
			body["suppress_run"] = true
		}
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+issueRef.ID, body, &result); err != nil {
		return fmt.Errorf("assign issue: %w", err)
	}

	if unassign {
		fmt.Fprintf(os.Stderr, "Issue %s unassigned.\n", issueDisplayKey(result))
	} else {
		fmt.Fprintf(os.Stderr, "Issue %s assigned to %s.\n", issueDisplayKey(result), displayTarget)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

// runIssueStatus 执行 `issue status <id> <key>`：把 issue 改到指定状态列。key 只做
// 本地形态校验（真正合法全集交给 server），默认会触发一次 agent run，--no-start 关闭。
func runIssueStatus(cmd *cobra.Command, args []string) error {
	id := args[0]
	status := args[1]
	noStart, _ := cmd.Flags().GetBool("no-start")

	if err := validateIssueStatus(status); err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, id)
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{"status": status}
	if noStart {
		body["suppress_run"] = true
	}
	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+issueRef.ID, body, &result); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Issue %s status changed to %s.\n", issueDisplayKey(result), status)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Reorder command —— 状态列内重新排序
// ---------------------------------------------------------------------------

// registerIssueReorderFlags 给 reorder 命令接上 flag，并把它的目标选择器声明成一组
// cobra flag group：互斥（--before/--after/--top/--bottom 至多接受一个）且必选其一。
// 用声明式规则代替在 runIssueReorder 里手数 flag，cobra 就能在 RunE 之前用标准报错
// 文案拒绝「零目标」和「多目标」调用，还能让 shell 补全感知分组——一旦某个目标已设，
// 就隐藏其余兄弟 flag。该函数被命令测试共用，测试跑的就是真正随命令发布的 flag 接线。
func registerIssueReorderFlags(cmd *cobra.Command) {
	cmd.Flags().String("before", "", "Place the issue directly above this issue (same column)")
	cmd.Flags().String("after", "", "Place the issue directly below this issue (same column)")
	cmd.Flags().Bool("top", false, "Move the issue to the top of its status column")
	cmd.Flags().Bool("bottom", false, "Move the issue to the bottom of its status column")
	cmd.Flags().String("output", "json", "Output format: table or json")
	cmd.MarkFlagsMutuallyExclusive("before", "after", "top", "bottom")
	cmd.MarkFlagsOneRequired("before", "after", "top", "bottom")
}

// runIssueReorder 在 issue 当前所处的状态列内重新排位。新位置由
// computeReorderPosition 在客户端算好——它复刻了看板/列表拖拽的排序算法
// （packages/views/issues/utils/drag-utils.ts 里的 computePosition），保证 CLI 与
// UI 对「issue 落在哪」看法一致。只改动 issue 自身的 position；它属于哪一列
// （status）不受影响，跨列移动仍走 `issue status`。
func runIssueReorder(cmd *cobra.Command, args []string) error {
	before, _ := cmd.Flags().GetString("before")
	after, _ := cmd.Flags().GetString("after")
	top, _ := cmd.Flags().GetBool("top")
	bottom, _ := cmd.Flags().GetBool("bottom")

	// 「--before/--after/--top/--bottom 恰好一个」由命令上的互斥+必选 flag group
	// 声明式兜底（见 registerIssueReorderFlags），cobra 会在 RunE 之前拒绝零目标与
	// 多目标调用。cobra 判断依据是 flag 的「出现与否」（Changed）而非值，所以这里
	// 再防它看不到的情况：--before/--after 传了空串（比如未赋值的 shell 变量），或
	// --top/--bottom 被显式置为 false（如生成命令里的 `--top=false`）。这些对 group
	// 而言都算「已设置」，却没有选中任何真实目标，若不拦下会落进一句令人困惑的
	// 「在该列找不到」错误。
	if cmd.Flags().Changed("before") && before == "" {
		return fmt.Errorf("--before requires an issue ID or key")
	}
	if cmd.Flags().Changed("after") && after == "" {
		return fmt.Errorf("--after requires an issue ID or key")
	}
	if cmd.Flags().Changed("top") && !top {
		return fmt.Errorf("--top cannot be set to false; pass it on its own to move the issue to the top of its column")
	}
	if cmd.Flags().Changed("bottom") && !bottom {
		return fmt.Errorf("--bottom cannot be set to false; pass it on its own to move the issue to the bottom of its column")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	wsID := client.WorkspaceID
	if wsID == "" {
		wsID, err = requireWorkspaceID(cmd)
		if err != nil {
			return err
		}
	}

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	target, err := fetchIssue(ctx, client, issueRef.ID)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}
	targetDisplay := issueDisplayKey(target)
	status := strVal(target, "status")
	if status == "" {
		return fmt.Errorf("issue %s has no status, cannot determine its column", targetDisplay)
	}

	// 在任何 no-op 捷径之前先把相对目标解析出来：这样坏的 --before/--after 值
	// （拼写错误、指向自身）总会报错，而不是被下面「单 issue 列」的快路径吞掉。
	relative := before != "" || after != ""
	var otherRef resolvedID
	if relative {
		otherInput := before
		if after != "" {
			otherInput = after
		}
		otherRef, err = resolveIssueRef(ctx, client, otherInput)
		if err != nil {
			return fmt.Errorf("resolve target issue: %w", err)
		}
		if otherRef.ID == issueRef.ID {
			return fmt.Errorf("cannot reorder issue %s relative to itself", targetDisplay)
		}
	}

	// 把列范围限定到 issue 自己的项目（若有）：这样算出的位置落在这个 issue 所在的
	// 项目看板上，而不是把该状态下列所有项目的问题混在一起算。
	projectID := strVal(target, "project_id")
	column, err := fetchIssueColumn(ctx, client, wsID, projectID, status)
	if err != nil {
		return fmt.Errorf("list %s column: %w", status, err)
	}

	// 构造去掉目标后的列顺序，外加一张「该列每个 issue 的位置」查找表
	// （目标自身的位置也保留，供下面的 no-op 判断用）。
	positions := make(map[string]float64, len(column))
	ordered := make([]string, 0, len(column))
	for _, raw := range column {
		id := strVal(raw, "id")
		if id == "" {
			continue
		}
		positions[id] = floatVal(raw, "position")
		if id != issueRef.ID {
			ordered = append(ordered, id)
		}
	}
	if len(ordered) == 0 {
		// 该 issue 独自占着整列。相对移动在这里必然失败（它的目标一定在别的列），
		// 与其报一句误导性的「没有可排序的内容」，不如直接讲明原因。
		if relative {
			return reorderTargetNotInColumnError(ctx, client, otherRef, targetDisplay, status)
		}
		fmt.Fprintf(os.Stderr, "Issue %s is the only issue in the %s column; nothing to reorder.\n", targetDisplay, status)
		return issueReorderOutput(cmd, target)
	}

	insertIdx := 0
	switch {
	case top:
		insertIdx = 0
	case bottom:
		insertIdx = len(ordered)
	default:
		idx := indexOfString(ordered, otherRef.ID)
		if idx == -1 {
			return reorderTargetNotInColumnError(ctx, client, otherRef, targetDisplay, status)
		}
		if before != "" {
			insertIdx = idx
		} else {
			insertIdx = idx + 1
		}
	}

	reordered := make([]string, 0, len(ordered)+1)
	reordered = append(reordered, ordered[:insertIdx]...)
	reordered = append(reordered, issueRef.ID)
	reordered = append(reordered, ordered[insertIdx:]...)

	currentPos := positions[issueRef.ID]
	newPos := computeReorderPosition(reordered, issueRef.ID, positions, currentPos)
	if newPos == currentPos {
		fmt.Fprintf(os.Stderr, "Issue %s is already in that position.\n", targetDisplay)
		return issueReorderOutput(cmd, target)
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID), map[string]any{"position": newPos}, &result); err != nil {
		return fmt.Errorf("reorder issue: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Issue %s reordered.\n", issueDisplayKey(result))
	return issueReorderOutput(cmd, result)
}

// issueReorderOutput 按 update 命令的输出契约，把 issue map 打成表格或 JSON 打印。
func issueReorderOutput(cmd *cobra.Command, issue map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"KEY", "TITLE", "STATUS", "PRIORITY"}
		rows := [][]string{{
			issueDisplayKey(issue),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}
	return cli.PrintJSON(os.Stdout, issue)
}

// reorderTargetNotInColumnError 解释为什么 --before/--after 的目标用不上。它额外
// 拉取一次目标仅为了在报错里写明它实际所在的列，让最常见的错误（目标在别的列）
// 得到一个精确、可执行的提示，而不是干巴巴一句「找不到」。
func reorderTargetNotInColumnError(ctx context.Context, client *cli.APIClient, otherRef resolvedID, issueDisplay, status string) error {
	otherDisplay := otherRef.Display
	if other, err := fetchIssue(ctx, client, otherRef.ID); err == nil {
		otherDisplay = issueDisplayKey(other)
		if otherStatus := strVal(other, "status"); otherStatus != "" && otherStatus != status {
			return fmt.Errorf("issue %s is in the %q column but %s is in %q; move one with `multica issue status` first, or pick a target in the same column", otherDisplay, otherStatus, issueDisplay, status)
		}
	}
	return fmt.Errorf("issue %s was not found in the %q column", otherDisplay, status)
}

// fetchIssue 按规范 ID 取单个 issue。
func fetchIssue(ctx context.Context, client *cli.APIClient, id string) (map[string]any, error) {
	var issue map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(id), &issue); err != nil {
		return nil, err
	}
	return issue, nil
}

// fetchIssueColumn 返回某个状态列里的全部 issue，按 position 升序；通过 list 端点
// 翻页抓取，让超过一页（server 每页上限 100）的列也能拿到完整且顺序正确的集合。
// projectID 非空时把列限定到该项目（对应项目看板）；为空则列出整个工作区的该状态列。
func fetchIssueColumn(ctx context.Context, client *cli.APIClient, workspaceID, projectID, status string) ([]map[string]any, error) {
	var all []map[string]any
	seen := make(map[string]struct{})
	offset := 0
	for {
		params := url.Values{}
		params.Set("workspace_id", workspaceID)
		params.Set("status", status)
		if projectID != "" {
			params.Set("project_id", projectID)
		}
		params.Set("sort", "position")
		params.Set("limit", fmt.Sprintf("%d", issueListMaxPageSize))
		params.Set("offset", fmt.Sprintf("%d", offset))

		var result map[string]any
		if err := client.GetJSON(ctx, "/api/issues?"+params.Encode(), &result); err != nil {
			return nil, err
		}
		page, ok := result["issues"].([]any)
		if !ok {
			return nil, fmt.Errorf("invalid issue column response: expected an issues array")
		}
		for _, raw := range page {
			m, ok := raw.(map[string]any)
			if !ok || strVal(m, "id") == "" {
				return nil, fmt.Errorf("invalid issue in column response")
			}
			id := strVal(m, "id")
			if _, exists := seen[id]; exists {
				return nil, fmt.Errorf("issue column returned duplicate issue %s; retry the reorder", id)
			}
			seen[id] = struct{}{}
			all = append(all, m)
		}
		total, totalOK := result["total"].(float64)
		offset += len(page)
		// Older servers substitute the page length when COUNT fails. Such a
		// total cannot prove the column is complete. Without a usable count,
		// read through the empty page, including when the server uses smaller
		// pages; a short page does not establish its applied limit. Duplicate
		// IDs above reject a repeated page before any position is written.
		totalTrusted := totalOK && total > float64(len(page))
		if len(page) == 0 || (totalTrusted && float64(offset) >= total) {
			break
		}
	}
	return all, nil
}

// computeReorderPosition 算出把 activeID 放到 ids 中它当前索引处所需的位置值。
// 复刻 packages/views/issues/utils/drag-utils.ts 里的 computePosition：列顶 = 下一项
// 位置减 1，列底 = 上一项位置加 1，中间槽位取左右邻居的中点。当 activeID 独自占列
// （或不在列里）时返回 fallback，保持其位置不变。
func computeReorderPosition(ids []string, activeID string, positions map[string]float64, fallback float64) float64 {
	idx := indexOfString(ids, activeID)
	if idx == -1 || len(ids) == 1 {
		return fallback
	}
	if idx == 0 {
		return positions[ids[1]] - 1
	}
	if idx == len(ids)-1 {
		return positions[ids[idx-1]] + 1
	}
	return (positions[ids[idx-1]] + positions[ids[idx+1]]) / 2
}

// indexOfString 返回 target 在 s 中的下标，找不到返回 -1。
func indexOfString(s []string, target string) int {
	for i, v := range s {
		if v == target {
			return i
		}
	}
	return -1
}

func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// ---------------------------------------------------------------------------
// Comment commands —— 评论相关子命令
// ---------------------------------------------------------------------------

// runIssueCommentList 执行 `issue comment list <issue-id>`：先本地镜像 server 的
// flag 组合校验（--recent/--thread/--tail/--roots-only 等互斥与依赖关系），再按模式
// 请求评论列表。--fold 折叠默认开启以省 token，--compact/--summary 只在 JSON 输出
// 下生效。
func runIssueCommentList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	since, _ := cmd.Flags().GetString("since")
	thread, _ := cmd.Flags().GetString("thread")
	recent, _ := cmd.Flags().GetInt("recent")
	tail, _ := cmd.Flags().GetInt("tail")
	rootsOnly, _ := cmd.Flags().GetBool("roots-only")
	summary, _ := cmd.Flags().GetBool("summary")
	full, _ := cmd.Flags().GetBool("full")
	// Flags().Changed 能区分「用户没传 --recent」和「用户显式传了 --recent 0」
	// （或负数）。GetInt 的零值会把这两种情况混为一谈，若不区分，我们会悄悄丢掉
	// 非法值、退回默认的无参列表——这正是 PR #2787 二审时被点名的问题。--tail 沿用
	// 同样模式，也保证「--tail 0」（只要根评论）能与「没传 --tail」区分开。
	recentSet := cmd.Flags().Changed("recent")
	tailSet := cmd.Flags().Changed("tail")
	before, _ := cmd.Flags().GetString("before")
	beforeID, _ := cmd.Flags().GetString("before-id")

	// 把 server 端的组合规则在客户端镜像一份，让用户立刻拿到清晰的本地产错，而不是
	// 打一次 400 的往返。这些规则与 handler.ListComments
	// （server/internal/handler/comment.go）里的校验保持一致。
	if recentSet && recent <= 0 {
		return fmt.Errorf("--recent must be a positive integer")
	}
	if tailSet && tail < 0 {
		return fmt.Errorf("--tail must be a non-negative integer (0 returns just the thread root)")
	}
	if thread != "" && recentSet {
		return fmt.Errorf("--thread and --recent are mutually exclusive")
	}
	if rootsOnly && thread != "" {
		return fmt.Errorf("--roots-only and --thread are mutually exclusive")
	}
	if rootsOnly && recentSet {
		return fmt.Errorf("--roots-only and --recent are mutually exclusive")
	}
	if rootsOnly && tailSet {
		return fmt.Errorf("--roots-only and --tail are mutually exclusive")
	}
	if rootsOnly && before != "" {
		return fmt.Errorf("--roots-only does not support --before / --before-id")
	}
	if tailSet && thread == "" {
		return fmt.Errorf("--tail requires --thread (it is a thread-scoped limit)")
	}
	if (before == "") != (beforeID == "") {
		return fmt.Errorf("--before and --before-id must be set together (composite cursor for stable pagination)")
	}
	if before != "" && !recentSet && !(thread != "" && tailSet) {
		return fmt.Errorf("--before / --before-id require --recent (thread cursor) or --thread + --tail (reply cursor)")
	}

	params := url.Values{}
	if since != "" {
		params.Set("since", since)
	}
	if rootsOnly {
		params.Set("roots_only", "true")
	}
	if summary {
		params.Set("summary", "true")
	}
	// 完整线程类读取（默认列表、--recent、不带 --tail 的 --thread）默认做「感知已解决
	// 的折叠」：已解决的线程塌缩成 根评论+结论，agent 不必为已尘埃落定的讨论付 token。
	// --full 可退出该行为。局部读取（--since / --tail）与 --roots-only 无法安全折叠
	// （server 会在那里拒绝 fold），所以对它们绝不发 fold，此时 --full 是无害的 no-op。
	// 只在具备折叠资格时发送 fold，保证所有既有读命令——包括提示词里的冷路径
	// `--thread <id> --tail 30`——行为不变地照常工作。
	foldEligible := !rootsOnly && since == "" && !tailSet
	if foldEligible && !full {
		params.Set("fold", "true")
	}
	if thread != "" {
		params.Set("thread", thread)
	}
	if tailSet {
		params.Set("tail", fmt.Sprintf("%d", tail))
	}
	if recentSet {
		params.Set("recent", fmt.Sprintf("%d", recent))
	}
	if before != "" {
		params.Set("before", before)
		params.Set("before_id", beforeID)
	}

	path := "/api/issues/" + issueRef.ID + "/comments"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var comments []map[string]any
	respHeaders, err := client.GetJSONWithHeaders(ctx, path, &comments)
	if err != nil {
		return fmt.Errorf("list comments: %w", err)
	}
	// 服务端在「很可能还有更早的一页」时把下一页游标放进响应头。把它打印到 stderr，
	// 让操作者（以及随本 PR 更新的 agent 提示词）不用翻原始 HTTP 响应就能继续往下翻。
	// 标签取决于调用方处于哪种翻页模式——--recent 下是线程游标；--thread + --tail
	// 下是该线程内的回复游标。
	if nb := respHeaders.Get("X-Multica-Next-Before"); nb != "" {
		if nbid := respHeaders.Get("X-Multica-Next-Before-Id"); nbid != "" {
			label := "Next thread cursor"
			if thread != "" && tailSet {
				label = "Next reply cursor"
			}
			fmt.Fprintf(os.Stderr, "%s: --before %s --before-id %s\n", label, nb, nbid)
		}
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		if compact, _ := cmd.Flags().GetBool("compact"); compact {
			compactComments(comments)
		}
		return cli.PrintJSON(os.Stdout, comments)
	}

	actors := loadActorDisplayLookup(ctx, client)
	headers := []string{"ID", "PARENT", "AUTHOR", "TYPE", "CONTENT", "CREATED"}
	rows := make([][]string, 0, len(comments))
	for _, c := range comments {
		content := strVal(c, "content")
		if utf8.RuneCountInString(content) > 80 {
			runes := []rune(content)
			content = string(runes[:77]) + "..."
		}
		created := strVal(c, "created_at")
		if len(created) >= 16 {
			created = created[:16]
		}
		parentID := strVal(c, "parent_id")
		if parentID == "" {
			parentID = "—"
		}
		rows = append(rows, []string{
			strVal(c, "id"),
			parentID,
			actors.actor(strVal(c, "author_type"), strVal(c, "author_id")),
			strVal(c, "type"),
			content,
			created,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runIssueCommentAdd 执行 `issue comment add <issue-id>`：解析内容来源（行内/
// stdin/文件）并做本地路径链接守卫，随后先上传全部本地附件再 POST 创建评论——
// 附件任一失败即中止，避免上传成孤儿附件却没有评论主体。
func runIssueCommentAdd(cmd *cobra.Command, args []string) error {
	content, hasContent, err := resolveTextFlag(cmd, "content")
	if err != nil {
		return err
	}
	if !hasContent {
		return fmt.Errorf("--content, --content-stdin, or --content-file is required")
	}
	if err := guardLocalPathLinks(content, "comment body",
		"Deliver the file itself with `multica issue comment add <issue-id> --attachment <path>` (repeatable) and drop the link."); err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	// 带了附件时放宽超时——文件上传可能很慢。
	timeout := cli.APITimeout()
	attachments, _ := cmd.Flags().GetStringSlice("attachment")
	if len(attachments) > 0 {
		timeout = cli.AtLeastAPITimeout(60 * time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	issueID := issueRef.ID

	// 在任何上传前先校验并读完全部附件。URL 形值给出警告后跳过——`--attachment`
	// 只接受本地文件路径。提前全部读入，意味着后面任一路径非法（被工作目录守卫拦下
	// 的外部/软链逃逸）都会在零上传的状态下中止调用，而不是留下「前面的附件已上传成
	// 孤儿、评论却没发出」的局面（重试会重复——MUL-4252）。
	pending, err := collectLocalAttachments(cmd, attachments)
	if err != nil {
		return err
	}
	var attachmentIDs []string
	for _, att := range pending {
		id, uploadErr := client.UploadFile(ctx, att.data, att.path, issueID)
		if uploadErr != nil {
			return fmt.Errorf("upload attachment %s: %w", att.path, uploadErr)
		}
		attachmentIDs = append(attachmentIDs, id)
		fmt.Fprintf(os.Stderr, "Uploaded %s\n", att.path)
	}

	body := map[string]any{"content": content}
	if parentID, _ := cmd.Flags().GetString("parent"); parentID != "" {
		body["parent_id"] = parentID
	}
	if len(attachmentIDs) > 0 {
		body["attachment_ids"] = attachmentIDs
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueID+"/comments", body, &result); err != nil {
		return fmt.Errorf("add comment: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Comment added to issue %s.\n", issueRef.Display)

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueCommentUpdate(cmd *cobra.Command, args []string) error {
	expectedRevision, _ := cmd.Flags().GetInt64("expected-revision")
	if !cmd.Flags().Changed("expected-revision") || expectedRevision < 1 {
		return fmt.Errorf("--expected-revision is required and must be a positive integer; read the current revision with `multica issue comment list <issue-id> --output json`")
	}

	content, hasContent, err := resolveTextFlag(cmd, "content")
	if err != nil {
		return err
	}
	if !hasContent {
		return fmt.Errorf("--content, --content-stdin, or --content-file is required")
	}
	if err := guardLocalPathLinks(content, "comment body",
		"Comment updates cannot attach files; deliver the file in a new comment with `multica issue comment add <issue-id> --attachment <path>` and drop the link."); err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	commentID := args[0]
	var result map[string]any
	if err := client.PutJSON(ctx, "/api/comments/"+url.PathEscape(commentID), map[string]any{
		"content":           content,
		"expected_revision": expectedRevision,
	}, &result); err != nil {
		return fmt.Errorf("update comment: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Comment %s updated.\n", commentID)
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runIssueCommentDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/comments/"+args[0]); err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Comment %s deleted.\n", args[0])
	return nil
}

func runIssueCommentResolve(cmd *cobra.Command, args []string) error {
	return runIssueCommentResolution(cmd, args[0], true)
}

func runIssueCommentUnresolve(cmd *cobra.Command, args []string) error {
	return runIssueCommentResolution(cmd, args[0], false)
}

// runIssueCommentResolution 是 resolve/unresolve 的共用实现：resolve=true 走 POST
// /resolve，否则走 DELETE /resolve。带附件上传场景下超时可能较长。
func runIssueCommentResolution(cmd *cobra.Command, commentID string, resolve bool) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	path := "/api/comments/" + url.PathEscape(commentID) + "/resolve"
	var result map[string]any
	if resolve {
		if err := client.PostJSON(ctx, path, nil, &result); err != nil {
			return fmt.Errorf("resolve comment: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Comment %s resolved.\n", commentID)
	} else {
		if err := client.DeleteJSONResponse(ctx, path, &result); err != nil {
			return fmt.Errorf("unresolve comment: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Comment %s unresolved.\n", commentID)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

// ---------------------------------------------------------------------------
// Execution history commands —— 执行历史与聚合用量
// ---------------------------------------------------------------------------

// runIssueRuns 执行 `issue runs <issue-id>`：列出该 issue 上的执行历史（task runs）；
// 表格输出会把 agent_id 渲染成名字、时间裁到分钟、错误信息截断显示。
func runIssueRuns(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	activeOnly, _ := cmd.Flags().GetBool("active")
	siblings, _ := cmd.Flags().GetBool("siblings")

	params := url.Values{}
	if siblings {
		// The server implies active for a family read, so --siblings alone is
		// enough; sending both keeps the request self-describing.
		params.Set("scope", "family")
		params.Set("active", "true")
	} else if activeOnly {
		params.Set("active", "true")
	}

	path := "/api/issues/" + issueRef.ID + "/task-runs"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var runs []map[string]any
	respHeaders, err := client.GetJSONWithHeaders(ctx, path, &runs)
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	// A truncated coordination read and a complete one look identical in the
	// body, and reading the first as the second is exactly the wrong
	// conclusion: "no run on that sibling" when the answer was simply cut off.
	// Same stderr convention as the comment-list paging cursors.
	if respHeaders.Get(headerActiveRunsTruncated) == "true" {
		fmt.Fprintln(os.Stderr,
			"warning: active runs truncated by the server cap: more runs are in flight than this read returns. "+
				"\"No run on that issue\" cannot be concluded from this read.")
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, runs)
	}

	actors := loadActorDisplayLookup(ctx, client)
	fullID, _ := cmd.Flags().GetBool("full-id")

	// The family read returns a different, deliberately smaller row than the
	// execution log — no completed_at or error, because every row in it is
	// still in flight — and it spans issues, so it needs a column saying which.
	// On a single-issue read that column would repeat the argument on every
	// line, so it stays off there.
	if siblings {
		headers := []string{"TASK", "ISSUE", "AGENT", "STATUS", "STARTED"}
		rows := make([][]string, 0, len(runs))
		for _, r := range runs {
			started := strVal(r, "started_at")
			if len(started) >= 16 {
				started = started[:16]
			}
			rows = append(rows, []string{
				displayID(strVal(r, "task_id"), fullID),
				strVal(r, "issue_identifier"),
				actors.agent(strVal(r, "agent_id")),
				strVal(r, "status"),
				started,
			})
		}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	headers := []string{"ID", "AGENT", "STATUS", "STARTED", "COMPLETED", "ERROR"}
	rows := make([][]string, 0, len(runs))
	for _, r := range runs {
		started := strVal(r, "started_at")
		if len(started) >= 16 {
			started = started[:16]
		}
		completed := strVal(r, "completed_at")
		if len(completed) >= 16 {
			completed = completed[:16]
		}
		errMsg := strVal(r, "error")
		if utf8.RuneCountInString(errMsg) > 50 {
			runes := []rune(errMsg)
			errMsg = string(runes[:47]) + "..."
		}
		rows = append(rows, []string{
			displayID(strVal(r, "id"), fullID),
			actors.agent(strVal(r, "agent_id")),
			strVal(r, "status"),
			started,
			completed,
			errMsg,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runIssueUsage 执行 `issue usage <issue-id>`：GET /usage 取该 issue 聚合后的
// token 用量（输入/输出/缓存读/缓存写）与任务数并输出。
func runIssueUsage(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/usage", &result); err != nil {
		return fmt.Errorf("get issue usage: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	// JSON 数字解出来是 float64；formatMetadataValue 把它们渲染成干净的整数
	// （大数值的缓存 token 数不会出现科学计数法）。
	headers := []string{"INPUT_TOKENS", "OUTPUT_TOKENS", "CACHE_READ", "CACHE_WRITE", "RUNS"}
	rows := [][]string{{
		formatMetadataValue(result["total_input_tokens"]),
		formatMetadataValue(result["total_output_tokens"]),
		formatMetadataValue(result["total_cache_read_tokens"]),
		formatMetadataValue(result["total_cache_write_tokens"]),
		formatMetadataValue(result["task_count"]),
	}}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runIssueRunMessages 执行 `issue run-messages <task-id>`：列出某次执行产生的对话
// 消息；--issue 限定短 task ID 的解析范围，--since 只取指定序号之后的消息。
func runIssueRunMessages(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueID := ""
	if issueInput, _ := cmd.Flags().GetString("issue"); issueInput != "" {
		issueRef, err := resolveIssueRef(ctx, client, issueInput)
		if err != nil {
			return fmt.Errorf("resolve issue: %w", err)
		}
		issueID = issueRef.ID
	}
	taskRef, err := resolveTaskRunID(ctx, client, issueID, args[0])
	if err != nil {
		return fmt.Errorf("resolve task run: %w", err)
	}

	path := "/api/tasks/" + url.PathEscape(taskRef.ID) + "/messages"
	if messageID, _ := cmd.Flags().GetString("message-output"); messageID != "" {
		offset, _ := cmd.Flags().GetInt("offset")
		if offset < 0 {
			return fmt.Errorf("output offset must be non-negative")
		}
		search, _ := cmd.Flags().GetString("search")
		query := url.Values{"output_id": {messageID}, "offset": {fmt.Sprint(offset)}, "q": {search}}
		var page map[string]any
		if err := client.GetJSON(ctx, path+"?"+query.Encode(), &page); err != nil {
			return fmt.Errorf("read run output: %w", err)
		}
		return cli.PrintJSON(os.Stdout, page)
	}
	if since, _ := cmd.Flags().GetInt("since"); since > 0 {
		path += fmt.Sprintf("?since=%d", since)
	}

	var messages []map[string]any
	if err := client.GetJSON(ctx, path, &messages); err != nil {
		return fmt.Errorf("list run messages: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, messages)
	}

	headers := []string{"SEQ", "TYPE", "TOOL", "CONTENT"}
	rows := make([][]string, 0, len(messages))
	for _, m := range messages {
		content := strVal(m, "content")
		if content == "" {
			content = strVal(m, "output")
		}
		if utf8.RuneCountInString(content) > 80 {
			runes := []rune(content)
			content = string(runes[:77]) + "..."
		}
		seq := ""
		if v, ok := m["seq"]; ok {
			seq = fmt.Sprintf("%v", v)
		}
		rows = append(rows, []string{
			seq,
			strVal(m, "type"),
			strVal(m, "tool"),
			content,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// Search command —— 全文搜索
// ---------------------------------------------------------------------------

// runIssueRerun 执行 `issue rerun <id>`：把该 issue 当前的 agent 指派重新入队成一条
// 全新任务（POST /rerun），相当于手动触发一次重新执行。
func runIssueRerun(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var task map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/rerun", map[string]any{}, &task); err != nil {
		return fmt.Errorf("rerun issue: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, task)
	}
	agent := loadActorDisplayLookup(ctx, client).agent(strVal(task, "agent_id"))
	fmt.Fprintf(os.Stdout, "Re-enqueued task %s on agent %s\n", strVal(task, "id"), agent)
	return nil
}

// runIssueCancelTask 按任务 ID 取消单个任务。接受 `issue runs` 显示的那种短 ID 前缀
// （经 resolveTaskRunID 解析）。走 /api/tasks/{taskId}/cancel，它既把 DB 行更新为
// status=cancelled，也触发 daemon 侧的中断路径（#2107）——飞行中的 agent 会立刻停止
// 发工具调用，而不是一直跑到自己超时。
func runIssueCancelTask(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueScope := ""
	if issueInput, _ := cmd.Flags().GetString("issue"); issueInput != "" {
		issueRef, err := resolveIssueRef(ctx, client, issueInput)
		if err != nil {
			return fmt.Errorf("resolve issue: %w", err)
		}
		issueScope = issueRef.ID
	}
	taskRef, err := resolveTaskRunID(ctx, client, issueScope, args[0])
	if err != nil {
		return fmt.Errorf("resolve task run: %w", err)
	}

	var result map[string]any
	path := "/api/tasks/" + url.PathEscape(taskRef.ID) + "/cancel"
	if err := client.PostJSON(ctx, path, map[string]any{}, &result); err != nil {
		return fmt.Errorf("cancel task: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	status := strVal(result, "status")
	if status == "" {
		status = "cancelled"
	}
	fmt.Fprintf(os.Stdout, "Task %s -> status=%s\n", taskRef.ID, status)
	return nil
}

// runIssueSearch 执行 `issue search <query>`：按标题/描述/评论正文做全文搜索；
// 表格输出额外展示命中来源（MATCH 列）与命中片段，方便判断相关度。
func runIssueSearch(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	params := url.Values{}
	params.Set("q", args[0])
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	if v, _ := cmd.Flags().GetBool("include-closed"); v {
		params.Set("include_closed", "true")
	}

	path := "/api/issues/search?" + params.Encode()

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("search issues: %w", err)
	}

	issuesRaw, _ := result["issues"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	headers := []string{"KEY", "TITLE", "STATUS", "MATCH"}
	rows := make([][]string, 0, len(issuesRaw))
	for _, raw := range issuesRaw {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		matchInfo := strVal(issue, "match_source")
		if snippet := strVal(issue, "matched_snippet"); snippet != "" {
			if utf8.RuneCountInString(snippet) > 50 {
				runes := []rune(snippet)
				snippet = string(runes[:47]) + "..."
			}
			matchInfo += ": " + snippet
		}
		rows = append(rows, []string{
			strVal(issue, "identifier"),
			strVal(issue, "title"),
			strVal(issue, "status"),
			matchInfo,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// Subscriber commands —— 订阅人管理
// ---------------------------------------------------------------------------

// runIssueSubscriberList 执行 `issue subscriber list <issue-id>`：列出该 issue 的
// 订阅者、订阅原因与时间。
func runIssueSubscriberList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var subscribers []map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/subscribers", &subscribers); err != nil {
		return fmt.Errorf("list subscribers: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, subscribers)
	}

	actors := loadActorDisplayLookup(ctx, client)
	headers := []string{"USER", "REASON", "CREATED"}
	rows := make([][]string, 0, len(subscribers))
	for _, s := range subscribers {
		created := strVal(s, "created_at")
		if len(created) >= 16 {
			created = created[:16]
		}
		rows = append(rows, []string{
			actors.actor(strVal(s, "user_type"), strVal(s, "user_id")),
			strVal(s, "reason"),
			created,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runIssueSubscriberAdd 执行 `issue subscriber add <issue-id>`：为 issue 增加一个
// 订阅人（member 或 agent），省略 --user/--user-id 时默认订阅调用者自己。
func runIssueSubscriberAdd(cmd *cobra.Command, args []string) error {
	return runIssueSubscriberMutation(cmd, args[0], "subscribe")
}

// runIssueSubscriberRemove 执行 `issue subscriber remove <issue-id>`：为 issue 移除
// 一个订阅人（member 或 agent），省略 --user/--user-id 时默认退订调用者自己。
func runIssueSubscriberRemove(cmd *cobra.Command, args []string) error {
	return runIssueSubscriberMutation(cmd, args[0], "unsubscribe")
}

// runIssueSubscriberMutation 是 subscribe/unsubscribe 的共用实现——两个端点接收相同
// 的请求体，仅路径不同。
func runIssueSubscriberMutation(cmd *cobra.Command, issueID, action string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, issueID)
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{}
	userName, _ := cmd.Flags().GetString("user")
	uType, uID, hasUser, resolveErr := pickAssigneeFromFlags(ctx, client, cmd, "user", "user-id", memberOrAgentKinds)
	if resolveErr != nil {
		return fmt.Errorf("resolve user: %w", resolveErr)
	}
	if hasUser {
		body["user_type"] = uType
		body["user_id"] = uID
	}

	var result map[string]any
	path := "/api/issues/" + issueRef.ID + "/" + action
	if err := client.PostJSON(ctx, path, body, &result); err != nil {
		return fmt.Errorf("%s issue: %w", action, err)
	}

	target := "caller"
	if userName != "" {
		target = userName
	} else if hasUser {
		target = loadActorDisplayLookup(ctx, client).actor(uType, uID)
	}
	if action == "subscribe" {
		fmt.Fprintf(os.Stderr, "Subscribed %s to issue %s.\n", target, issueRef.Display)
	} else {
		fmt.Fprintf(os.Stderr, "Unsubscribed %s from issue %s.\n", target, issueRef.Display)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

// ---------------------------------------------------------------------------
// Helpers —— 公共辅助函数：assignee 解析是核心，被 issue/project 等多组命令共用
// ---------------------------------------------------------------------------

// assigneeMatch 是一次 assignee 解析命中的候选：Type/ID 就是要写进请求体的
// assignee_type/assignee_id，Name 供报错与展示用。
type assigneeMatch struct {
	Type string // 实体类型："member" / "agent" / "squad"
	ID   string // 对应 UUID：member 是 user_id，agent 是 agent id，squad 是 squad id
	Name string
}

// assigneeKinds 表示某个 flag 允许解析成哪些实体类型。issue 的 assignee 三种都收
// （issueAssigneeKinds），而 project lead 与 issue 订阅者只收 member 或 agent
// （memberOrAgentKinds）——`project.lead_type` 上的 DB CHECK 与订阅者 handler 里
// 的 isWorkspaceEntity 分支都会拒绝 `squad`，对这些调用方解析成 (squad, ...) 只会
// 以 500 / 403 收场，而不是得到干净的 CLI 侧解析错误（MUL-2165 后续）。
type assigneeKinds struct {
	member, agent, squad bool
}

var (
	issueAssigneeKinds = assigneeKinds{member: true, agent: true, squad: true}
	memberOrAgentKinds = assigneeKinds{member: true, agent: true}
	// Actor 属性值只允许 member（MUL-6286）。
	memberOnlyKinds = assigneeKinds{member: true}
)

// assigneeResolveRetrySleep 是在 assignee 解析重试之间等待的可替换函数（测试会换成
// 假的）。返回 true 表示 ctx 已取消，调用方应停止重试。
var assigneeResolveRetrySleep = func(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

// getAssigneeJSON 带轻量重试地 GET 一个 JSON 端点（用于拉取 members/agents/squads）。
// 只对可重试的解析错误（见 isRetryableAssigneeResolveError，即网络层错误）按
// 100ms→250ms 退避最多重试 3 次；HTTP 4xx/5xx 不重试。重试等待期间 ctx 被取消则
// 直接返回 ctx.Err()。
func getAssigneeJSON(ctx context.Context, client *cli.APIClient, path string, out any) error {
	delays := []time.Duration{100 * time.Millisecond, 250 * time.Millisecond}
	var err error
	for attempt := 0; attempt <= len(delays); attempt++ {
		err = client.GetJSON(ctx, path, out)
		if err == nil || !isRetryableAssigneeResolveError(err) || attempt == len(delays) {
			return err
		}
		if assigneeResolveRetrySleep(ctx, delays[attempt]) {
			return ctx.Err()
		}
	}
	return err
}

// isRetryableAssigneeResolveError 判断错误是否值得在解析 assignee 时重试：
// 目前只有网络层错误算——连不上/超时这类瞬时故障才有重试价值。
func isRetryableAssigneeResolveError(err error) bool {
	var netErr *cli.NetworkError
	return errors.As(err, &netErr)
}

func (k assigneeKinds) describe() string {
	parts := make([]string, 0, 3)
	if k.member {
		parts = append(parts, "member")
	}
	if k.agent {
		parts = append(parts, "agent")
	}
	if k.squad {
		parts = append(parts, "squad")
	}
	switch len(parts) {
	case 0:
		return "<none>"
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", or " + parts[len(parts)-1]
	}
}

// assigneeCandidate is one directory entry matchAssignee ranks. aliases are
// additional unique identifiers that select a candidate outright, ranked with
// id matches rather than name matches — a member's email is as unambiguous as
// their id, and is what people actually have to hand. Without it,
// `--value bohan@example.com` fails to resolve.
type assigneeCandidate struct {
	assigneeMatch
	aliases []string
}

func (c assigneeCandidate) matchesAlias(input string) bool {
	for _, alias := range c.aliases {
		if alias != "" && strings.EqualFold(alias, input) {
			return true
		}
	}
	return false
}

func memberCandidates(members []map[string]any) []assigneeCandidate {
	candidates := make([]assigneeCandidate, 0, len(members))
	for _, m := range members {
		candidates = append(candidates, assigneeCandidate{
			assigneeMatch: assigneeMatch{Type: "member", ID: strVal(m, "user_id"), Name: strVal(m, "name")},
			aliases:       []string{strVal(m, "email")},
		})
	}
	return candidates
}

func resolveAssignee(ctx context.Context, client *cli.APIClient, name string, kinds assigneeKinds) (string, string, error) {
	if client.WorkspaceID == "" {
		return "", "", fmt.Errorf("workspace ID is required to resolve assignees; use --workspace-id or set MULTICA_WORKSPACE_ID")
	}
	if normalizeAssigneeLookupInput(name) == "" {
		return "", "", fmt.Errorf("no %s found matching %q", kinds.describe(), name)
	}

	var candidates []assigneeCandidate
	var errs []error
	var fetchAttempts int

	// Search members.
	if kinds.member {
		fetchAttempts++
		var members []map[string]any
		if err := getAssigneeJSON(ctx, client, "/api/workspaces/"+client.WorkspaceID+"/members", &members); err != nil {
			errs = append(errs, fmt.Errorf("fetch members: %w", err))
		} else {
			candidates = append(candidates, memberCandidates(members)...)
		}
	}

	// 搜 agents。
	if kinds.agent {
		fetchAttempts++
		var agents []map[string]any
		agentPath := "/api/agents?" + url.Values{"workspace_id": {client.WorkspaceID}}.Encode()
		if err := getAssigneeJSON(ctx, client, agentPath, &agents); err != nil {
			errs = append(errs, fmt.Errorf("fetch agents: %w", err))
		} else {
			for _, a := range agents {
				candidates = append(candidates, assigneeCandidate{assigneeMatch: assigneeMatch{Type: "agent", ID: strVal(a, "id"), Name: strVal(a, "name")}})
			}
		}
	}

	// 搜 squads。平台允许把 issue 指派给 squad（由 leader agent 协调派活），所以
	// squad 名字也必须能在这里解析成 issue 的 assignee——否则用户说「assign to
	// <SquadName>」会静默落空，autopilot 提示词最终打出「Unrecognized assignee:
	// <SquadName>」（MUL-2165）。目标 schema 只收 member-or-agent 的调用方
	// （project lead、subscriber）必须通过 `kinds.squad = false` 退出。
	if kinds.squad {
		fetchAttempts++
		var squads []map[string]any
		if err := getAssigneeJSON(ctx, client, "/api/squads", &squads); err != nil {
			errs = append(errs, fmt.Errorf("fetch squads: %w", err))
		} else {
			for _, s := range squads {
				if strVal(s, "archived_at") != "" {
					continue
				}
				candidates = append(candidates, assigneeCandidate{assigneeMatch: assigneeMatch{Type: "squad", ID: strVal(s, "id"), Name: strVal(s, "name")}})
			}
		}
	}

	// 若每个列表都拉取失败，就报告这些错误，而不是一句误导性的「未找到」。
	if fetchAttempts > 0 && len(errs) == fetchAttempts {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return "", "", fmt.Errorf("failed to resolve assignee: %s", strings.Join(msgs, "; "))
	}

	return matchAssignee(name, kinds, candidates)
}

// matchAssignee resolves name against candidates a caller already fetched.
// Matches are collected into three priority buckets. Higher-priority buckets
// short-circuit lower-priority matching so that, e.g., an exact name match
// always wins over a substring collision with another candidate.
//  1. idMatches        — full UUID or 8-char ShortID (as shown by `truncateID`), or an alias.
//  2. exactMatches     — case-insensitive full name equality.
//  3. substringMatches — preserves the existing partial-name UX.
func matchAssignee(name string, kinds assigneeKinds, candidates []assigneeCandidate) (string, string, error) {
	input := normalizeAssigneeLookupInput(name)
	if input == "" {
		return "", "", fmt.Errorf("no %s found matching %q", kinds.describe(), name)
	}
	inputLower := strings.ToLower(input)

	var idMatches, exactMatches, substringMatches []assigneeMatch
	for _, c := range candidates {
		switch {
		case c.ID != "" && (strings.EqualFold(c.ID, input) || strings.EqualFold(truncateID(c.ID), input)):
			idMatches = append(idMatches, c.assigneeMatch)
		case c.matchesAlias(input):
			idMatches = append(idMatches, c.assigneeMatch)
		case strings.EqualFold(c.Name, input):
			exactMatches = append(exactMatches, c.assigneeMatch)
		case strings.Contains(strings.ToLower(c.Name), inputLower):
			substringMatches = append(substringMatches, c.assigneeMatch)
		}
	}

	for _, bucket := range [][]assigneeMatch{idMatches, exactMatches, substringMatches} {
		switch len(bucket) {
		case 0:
			continue
		case 1:
			return bucket[0].Type, bucket[0].ID, nil
		default:
			return "", "", ambiguousAssigneeError(input, bucket)
		}
	}
	return "", "", fmt.Errorf("no %s found matching %q", kinds.describe(), input)
}

// normalizeAssigneeLookupInput 预处理用户输入的 assignee 名字：去掉首尾空白、剥掉
// 开头的 @ 或全角＠；若整体形如 `@member:xxx` / `@agent:xxx` / `@squad:xxx`（util 的
// MentionRe 识别的提及语法），只留下后面的名字部分参与匹配。
func normalizeAssigneeLookupInput(raw string) string {
	input := strings.TrimSpace(raw)
	if m := util.MentionRe.FindStringSubmatch(input); len(m) == 4 && m[0] == input {
		switch m[2] {
		case "member", "agent", "squad":
			return m[3]
		}
	}
	input = strings.TrimLeftFunc(input, func(r rune) bool {
		return r == '@' || r == '＠'
	})
	return strings.TrimSpace(input)
}

// ambiguousAssigneeError 把多个歧义命中整理成带候选清单的报错。
func ambiguousAssigneeError(input string, matches []assigneeMatch) error {
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, fmt.Sprintf("  %s %q (%s)", m.Type, m.Name, truncateID(m.ID)))
	}
	return fmt.Errorf("ambiguous assignee %q; matches:\n%s", input, strings.Join(parts, "\n"))
}

// resolveAssigneeByID 严格地按规范 UUID 反查 (assignee_type, assignee_id)：在
// 工作区的 members、agents、（允许时）squads 里逐项比对 ID。它是 resolveAssignee 的
// 确定性对应物——已经手握 UUID 的调用方（例如 agent 从 `multica workspace member list
// --output json` 里读到的 ID）应该用它，而不是再走一遍名字匹配，后者在名字重叠的
// 工作区里可能产生歧义。
func resolveAssigneeByID(ctx context.Context, client *cli.APIClient, id string, kinds assigneeKinds) (string, string, error) {
	if client.WorkspaceID == "" {
		return "", "", fmt.Errorf("workspace ID is required to resolve assignees; use --workspace-id or set MULTICA_WORKSPACE_ID")
	}
	input := strings.TrimSpace(id)
	if !uuidRegexp.MatchString(input) {
		return "", "", fmt.Errorf("expected a canonical UUID, got %q", id)
	}

	var members []map[string]any
	var memberErr error
	if kinds.member {
		memberErr = getAssigneeJSON(ctx, client, "/api/workspaces/"+client.WorkspaceID+"/members", &members)
	}

	var agents []map[string]any
	var agentErr error
	if kinds.agent {
		agentPath := "/api/agents?" + url.Values{"workspace_id": {client.WorkspaceID}}.Encode()
		agentErr = getAssigneeJSON(ctx, client, agentPath, &agents)
	}

	var squads []map[string]any
	var squadErr error
	if kinds.squad {
		squadErr = getAssigneeJSON(ctx, client, "/api/squads", &squads)
	}

	allFailed := true
	hasFetch := false
	for _, pair := range []struct {
		enabled bool
		err     error
	}{{kinds.member, memberErr}, {kinds.agent, agentErr}, {kinds.squad, squadErr}} {
		if !pair.enabled {
			continue
		}
		hasFetch = true
		if pair.err == nil {
			allFailed = false
		}
	}
	if hasFetch && allFailed {
		return "", "", fmt.Errorf("failed to resolve assignee: %v; %v; %v", memberErr, agentErr, squadErr)
	}

	for _, m := range members {
		if strings.EqualFold(strVal(m, "user_id"), input) {
			return "member", strVal(m, "user_id"), nil
		}
	}
	for _, a := range agents {
		if strings.EqualFold(strVal(a, "id"), input) {
			return "agent", strVal(a, "id"), nil
		}
	}
	for _, s := range squads {
		if strings.EqualFold(strVal(s, "id"), input) {
			return "squad", strVal(s, "id"), nil
		}
	}

	return "", "", fmt.Errorf("no %s found with ID %q", kinds.describe(), input)
}

// pickAssigneeFromFlags 从 cmd 上读取一对 (名字 flag, ID flag) 并解析成
// (assignee_type, assignee_id)，实体范围限制在 kinds 内。第三个返回值表示两个 flag
// 是否「被显式设置」——调用方据此决定要不要把 assignee_* 写进请求体。两个 flag 互斥：
// 同时传会在最前面被拒绝，脚本不小心都设了也不会悄悄二选一。
//
// 「是否设置」用 Flags().Changed 判断（而不是看值是否为空）：脚本插入空环境变量
// （`--assignee-id "$MAYBE_UUID"`）时，必须通过 resolveAssignee/resolveAssigneeByID
// 大声失败，而不是悄悄退化成「无过滤 / 未指派 / 订阅调用者」——那会让新 flag 存在的
// 严格 UUID 保证失去意义。
func pickAssigneeFromFlags(ctx context.Context, client *cli.APIClient, cmd *cobra.Command, nameFlag, idFlag string, kinds assigneeKinds) (string, string, bool, error) {
	nameSet := cmd.Flags().Changed(nameFlag)
	idSet := cmd.Flags().Changed(idFlag)
	if nameSet && idSet {
		return "", "", false, fmt.Errorf("--%s and --%s are mutually exclusive", nameFlag, idFlag)
	}
	if idSet {
		idVal, _ := cmd.Flags().GetString(idFlag)
		t, i, err := resolveAssigneeByID(ctx, client, idVal, kinds)
		if err != nil {
			return "", "", true, err
		}
		return t, i, true, nil
	}
	if nameSet {
		name, _ := cmd.Flags().GetString(nameFlag)
		t, i, err := resolveAssignee(ctx, client, name, kinds)
		if err != nil {
			return "", "", true, err
		}
		return t, i, true, nil
	}
	return "", "", false, nil
}

// formatAssignee 从 issue 响应里取 assignee_type+assignee_id，再经 actor 查找表渲染成
// 可读的展示名；issue 未被指派时返回空串。
func formatAssignee(issue map[string]any, actors actorDisplayLookup) string {
	aType := strVal(issue, "assignee_type")
	aID := strVal(issue, "assignee_id")
	if aType == "" || aID == "" {
		return ""
	}
	return actors.actor(aType, aID)
}

// filterIssueFields keeps only the requested top-level keys on each issue,
// dropping everything else — including description, which makes up most of
// a typical issue payload. Opt-in via --fields on JSON output only.
func filterIssueFields(issuesRaw []any, fields []string) {
	keep := make(map[string]bool, len(fields))
	for _, f := range fields {
		keep[f] = true
	}
	for _, raw := range issuesRaw {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for k := range issue {
			if !keep[k] {
				delete(issue, k)
			}
		}
	}
}

func truncateID(id string) string {
	if utf8.RuneCountInString(id) > 8 {
		runes := []rune(id)
		return string(runes[:8])
	}
	return id
}

// compactComments 从评论响应里剥离对读者无信息的字段：从请求路径原样回显的 issue_id、
// 每次运行的记账字段（source_task_id）、与 created_at 相同的 updated_at、值为 null 的
// 键以及空数组。其余一切——内容、身份、线程汇总字段——原样保留。通过 --compact 开启，
// 只作用于 JSON 输出：agent 工作流规定的两种有界读取恰好被这类元数据主导（一次
// --roots-only --summary 扫描中占 56%，一次 --thread --tail 读取中占 21%，在生产
// issue 上实测），而且会随提示词缓存前缀持续放大（#5999 后续，MUL-5442）。
func compactComments(comments []map[string]any) {
	for _, c := range comments {
		delete(c, "issue_id")
		delete(c, "source_task_id")
		if ua, ok := c["updated_at"]; ok && ua == c["created_at"] {
			delete(c, "updated_at")
		}
		for k, v := range c {
			switch vv := v.(type) {
			case nil:
				delete(c, k)
			case []any:
				if len(vv) == 0 {
					delete(c, k)
				}
			}
		}
	}
}
