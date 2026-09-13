// 文件职责：实现 `multica issue timeline`（别名 history）命令——把 issue 的
// activity 日志（状态/优先级/assignee/标题/日期变更，以及 server 写入的
// task_completed/task_failed 记录）与评论按时间从旧到新合并成一份时间线输出，
// 用来回答「这个 issue 是怎么走到现在这步的」「卡在当前状态多久了」这类问题。
// 过滤在客户端做（timelineFilter），因为端点一次返回全部、本身不收过滤参数。
package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// noneMarker 用于替代表格里「变更的一侧缺失」的情况——例如 issue 之前没指派、
// 或变更后被取消指派。
const noneMarker = "(none)"

// headerTimelineTruncated 与 handler.HeaderTimelineTruncated 对应。用字面量而非
// import，是因为 CLI 不依赖 handler 包（与 `issue comment list` 里
// X-Multica-Next-Before 游标头的做法一致）。值是逗号分隔的种类列表：
// "activity"、"comment" 或 "activity,comment"。
const headerTimelineTruncated = "X-Timeline-Truncated"

// issueTimelineCmd 是 `issue timeline <id>`（别名 history）命令，时间线用法与
// 语义见其 Long 帮助文本。
var issueTimelineCmd = &cobra.Command{
	Use:     "timeline <id>",
	Aliases: []string{"history"},
	Short:   "Chronological issue history — when status/assignee changed, how long it has been stuck",
	Long: `Chronological history of an issue: the activity log — status / priority /
assignee / title / date changes, plus whatever task_completed / task_failed
records the server already writes — merged with comments, oldest first.

Use it for the questions the current issue fields cannot answer:

  - When did this issue move to in_review?
  - How long has it been sitting in its current status?
  - What changed on this issue since yesterday?

For "what is the state RIGHT NOW", prefer ` + "`issue get`" + ` — that is the authoritative
current snapshot. This command explains how it got there, which comments alone
cannot: a status change writes an activity record, never a comment, so a merge
that flipped the issue to done leaves no trace in the comment stream.

Comments are included by default. --activity-only drops comment bodies and
keeps every activity record; to isolate the state transitions behind "how long
has this been stuck", filter by action instead.

The server caps how far back it will read. When it reports that the response
was truncated, a warning naming the affected kinds is printed to stderr —
durations and "first entered <status>" cannot be concluded from a truncated
read, because the transition you are looking for may be the one that fell off.

Examples:
  # How long has MUL-123 been in its current status?
  multica issue timeline MUL-123 --action status_changed

  # Every state change, no comment bodies
  multica issue timeline MUL-123 --activity-only --output json

  # What changed since yesterday?
  multica issue timeline MUL-123 --since 2026-08-19T00:00:00Z`,
	Args: exactArgs(1),
	RunE: runIssueTimeline,
}

func init() {
	issueTimelineCmd.Flags().String("output", "table", "Output format: table or json")
	issueTimelineCmd.Flags().Bool("activity-only", false, "Drop comments and return every activity record — including the task_completed / task_failed entries the server already writes, not just field changes. Much cheaper to read than the full timeline; use --action when you want only state transitions.")
	issueTimelineCmd.Flags().StringSlice("action", nil, "Only return activities with these actions (repeatable or comma-separated). Implies --activity-only, since comments carry no action. Known actions: created, status_changed, priority_changed, assignee_changed, title_changed, description_updated, start_date_changed, due_date_changed, task_completed, task_failed, squad_leader_evaluated.")
	issueTimelineCmd.Flags().String("since", "", "Only return entries created after this timestamp (RFC3339)")
	issueTimelineCmd.Flags().Int("tail", 0, "Only return the N most recent entries (applied after every other filter)")
	issueTimelineCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")

	issueCmd.AddCommand(issueTimelineCmd)
}

// runIssueTimeline 执行 `issue timeline <id>`：不带分页参数地整条拉取时间线
// （该端点只有在不带 limit/before/after/around 时才返回扁平的旧→新数组），随后
// 应用客户端过滤、检查 truncation 头并输出。
func runIssueTimeline(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	filter, err := timelineFilterFromFlags(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	// 特意不带 limit/before/after/around：端点只有在没有任何分页参数时才返回
	// 扁平、旧→新的完整数组。
	var entries []map[string]any
	respHeaders, err := client.GetJSONWithHeaders(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/timeline", &entries)
	if err != nil {
		return fmt.Errorf("list issue timeline: %w", err)
	}
	warnTimelineTruncated(os.Stderr, respHeaders.Get(headerTimelineTruncated))

	entries = filter.apply(entries)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, entries)
	}

	fullID, _ := cmd.Flags().GetBool("full-id")
	printIssueTimelineTable(entries, loadActorDisplayLookup(ctx, client), fullID)
	return nil
}

// warnTimelineTruncated 把 server 的「截断」信号写到 stderr，避免污染
// --output json 的管道输出。
//
// 端点按种类独立设上限，并用响应头报告哪类丢了行。吞掉这个信号会让一次「读少了」
// 看起来像「读全了」——对本命令的主打问题（卡在当前状态多久）恰恰是错的：
// 若「置成当前状态」的那次变更正好落在窗口外，--action status_changed 会给出一个
// 看起来合理、却更短的错误答案。
func warnTimelineTruncated(w io.Writer, kinds string) {
	if kinds == "" {
		return
	}
	fmt.Fprintf(w, "warning: timeline truncated by the server cap (%s): older entries are missing. "+
		"Durations and \"first entered <status>\" cannot be concluded from this read.\n", kinds)
}

// timelineFilter 在客户端收窄时间线响应。端点不收任何过滤参数、一次返回整条
// 时间线，所以这里的每个 flag 都在拉取之后应用：它限制的是「调用方要读多少」，
// 而不是「server 要做多少」。
type timelineFilter struct {
	activityOnly bool            // 只保留 activity 记录，丢弃评论。
	actions      map[string]bool // 只保留这些 action（如 status_changed）的 activity。
	since        time.Time       // 只保留晚于此时间（created_at）的条目。
	tail         int             // 只保留最近 N 条（在所有其它过滤之后应用）。
}

// timelineFilterFromFlags 从命令行 flag 构建 timelineFilter，并做本地参数校验
// （--tail >= 0、--since 必须是 RFC3339 时间）。
func timelineFilterFromFlags(cmd *cobra.Command) (timelineFilter, error) {
	var f timelineFilter

	f.activityOnly, _ = cmd.Flags().GetBool("activity-only")

	f.tail, _ = cmd.Flags().GetInt("tail")
	if f.tail < 0 {
		return f, fmt.Errorf("--tail must be >= 0")
	}

	actions, _ := cmd.Flags().GetStringSlice("action")
	for _, a := range actions {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		if f.actions == nil {
			f.actions = map[string]bool{}
		}
		f.actions[a] = true
	}
	if len(f.actions) > 0 {
		// 评论没有 action 字段，按 action 过滤只会命中 activity——顺手把它
		// 隐含成 activity-only，省得用户多传一个 flag。
		f.activityOnly = true
	}

	if raw, _ := cmd.Flags().GetString("since"); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, fmt.Errorf("invalid --since %q: expected RFC3339, e.g. 2026-08-19T00:00:00Z", raw)
		}
		f.since = ts
	}

	return f, nil
}

// apply 把过滤器套到一批条目上，返回满足全部条件的新切片。条目天然旧→新，
// tail 就直接取末尾切片。created_at 解析失败的时间线条目，无法判定是否落在
// --since 窗口内，时间受限的读取会把它们丢掉。
func (f timelineFilter) apply(entries []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if f.activityOnly && strVal(e, "type") != "activity" {
			continue
		}
		if len(f.actions) > 0 && !f.actions[strVal(e, "action")] {
			continue
		}
		if !f.since.IsZero() {
			// 时间戳解析不了的条目不可能被证明落在窗口内，时间受限读取会丢弃它。
			ts, err := time.Parse(time.RFC3339, strVal(e, "created_at"))
			if err != nil || !ts.After(f.since) {
				continue
			}
		}
		out = append(out, e)
	}

	// 条目旧→新到达，最近的 N 条就是末尾切片。
	if f.tail > 0 && len(out) > f.tail {
		out = out[len(out)-f.tail:]
	}
	return out
}

// printIssueTimelineTable 把时间线条目渲染成 TIME/TYPE/ACTOR/DETAIL 四列；
// TYPE 列对带 action 的 activity 显示 action（比字面 "activity" 更可读）。
func printIssueTimelineTable(entries []map[string]any, actors actorDisplayLookup, fullID bool) {
	headers := []string{"TIME", "TYPE", "ACTOR", "DETAIL"}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		when := strVal(e, "created_at")
		if len(when) >= 16 {
			when = when[:16]
		}
		// activity 用它的 action 标注比字面的 "activity" 可读得多。
		kind := strVal(e, "type")
		if action := strVal(e, "action"); action != "" {
			kind = action
		}
		rows = append(rows, []string{
			when,
			kind,
			timelineActor(strVal(e, "actor_type"), strVal(e, "actor_id"), actors, fullID),
			timelineDetail(e, actors, fullID),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
}

// timelineActor 把 actor 渲染成 "member:Alice"；工作区查找表叫不出名字时
// （成员已删除、外部 ID）回退到缩短后的 id。
func timelineActor(actorType, actorID string, actors actorDisplayLookup, fullID bool) string {
	switch {
	case actorType == "" && actorID == "":
		return ""
	case actorID == "":
		// 系统代写的 activity 常带 type 却没有 id，查找表对它们返回空。
		// 比如 GitHub PR 合并把 issue 翻成 done 就是这种形态——把这一列留空
		// 等于抹掉了本命令存在意义所在的变更主体。
		return actorType
	case actorType == "":
		if fullID {
			return actorID
		}
		return truncateID(actorID)
	}
	display := actors.actor(actorType, actorID)
	if !fullID && strings.HasSuffix(display, ":"+actorID) {
		return actorType + ":" + truncateID(actorID)
	}
	return display
}

// timelineDetail 渲染单条条目给表格看：评论取截断后的正文，activity 渲染成
// 「变更」。activity 的 details 是每种 action 自由格式的 JSON，认不出的形态
// 就按 key=value 排序拼出兜底文案，而不是丢弃。
func timelineDetail(entry map[string]any, actors actorDisplayLookup, fullID bool) string {
	if strVal(entry, "type") == "comment" {
		return clipTimelineText(singleLineText(strVal(entry, "content")), 60)
	}

	details, _ := entry["details"].(map[string]any)
	if len(details) == 0 {
		return ""
	}

	// status / priority / title / date 类变更：{"from": ..., "to": ...}
	if _, ok := details["from"]; ok {
		return transitionText(strVal(details, "from"), strVal(details, "to"))
	}
	if _, ok := details["to"]; ok {
		return transitionText("", strVal(details, "to"))
	}

	// assignee_changed：{"from_type","from_id","to_type","to_id"}——哪一侧没指派，
	// 那一侧的字段就不存在。
	if hasAnyKey(details, "from_type", "from_id", "to_type", "to_id") {
		return transitionText(
			timelineActor(strVal(details, "from_type"), strVal(details, "from_id"), actors, fullID),
			timelineActor(strVal(details, "to_type"), strVal(details, "to_id"), actors, fullID),
		)
	}

	keys := make([]string, 0, len(details))
	for k := range details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strVal(details, k))
	}
	return clipTimelineText(strings.Join(parts, " "), 60)
}

// transitionText 把变更前后拼成 "旧 → 新"，任一侧为空串时用 noneMarker 占位。
func transitionText(from, to string) string {
	if from == "" {
		from = noneMarker
	}
	if to == "" {
		to = noneMarker
	}
	return from + " → " + to
}

// hasAnyKey 判断 m 里是否至少出现一个给定 key。
func hasAnyKey(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// singleLineText 把一段文本折叠成单行（连续的空白/换行压成一个空格）。
func singleLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipTimelineText 把文本裁到最多 max 个字符（按 rune 数），超出的末尾加 "..."。
func clipTimelineText(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
