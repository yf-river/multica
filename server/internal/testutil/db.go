package testutil

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Cols 是一行夹具数据：列名到值。除非值是 Raw，否则都作为查询参数绑定。
type Cols map[string]any

// Raw 是被内联进 SQL 语句而不是作为参数绑定的表达式，用于夹具无法用 Go 值表达的
// 默认值——`now() - interval '2 hours'`，或某工作区里下一个 issue 号。
//
// 它原样内联。只允许用字面量与夹具自己产生的 id 构造它。
type Raw string

// Fixture 为一个测试工作区插入若干行，并在索要它们的测试结束前再删除。
//
// 零值不可用，需要 New。WorkspaceID 与 UserID 是所有 builder 回退的默认值，
// 因此只关心某一列的测试可以只点名那一列。
type Fixture struct {
	Pool        *pgxpool.Pool
	WorkspaceID string
	UserID      string
}

// New 返回绑定到"测试套件在 TestMain 里搭好的工作区与用户"的 Fixture。
func New(pool *pgxpool.Pool, workspaceID, userID string) *Fixture {
	return &Fixture{Pool: pool, WorkspaceID: workspaceID, UserID: userID}
}

// Insert 往 table 写入一行，把删除注册给 t.Cleanup，并返回新行的 id。
//
// Cleanup 按"创建顺序的逆序"执行，因此后插入的、引用了先插入行的行会先被删除，
// 外键保持成立，测试无需拼写拆卸顺序。
func (f *Fixture) Insert(t TB, table string, cols Cols) string {
	t.Helper()

	names, placeholders, args := compile(cols)
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING id",
		quoteIdent(table), strings.Join(names, ", "), strings.Join(placeholders, ", "))

	var id string
	if err := f.Pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("setup: insert into %s: %v", table, err)
	}
	f.deleteOnCleanup(t, table, id)
	return id
}

// InsertNoID 往"没有 id 列"的表（通常是关联表）写入一行，并用调用方给的 WHERE
// 条件删除。where 会拼到 `DELETE FROM <table> WHERE ` 之后，其参数从 $1 编号。
func (f *Fixture) InsertNoID(t TB, table string, cols Cols, where string, whereArgs ...any) {
	t.Helper()

	names, placeholders, args := compile(cols)
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteIdent(table), strings.Join(names, ", "), strings.Join(placeholders, ", "))
	if _, err := f.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("setup: insert into %s: %v", table, err)
	}

	t.Cleanup(func() {
		_, _ = f.Pool.Exec(context.Background(),
			fmt.Sprintf("DELETE FROM %s WHERE %s", quoteIdent(table), where), whereArgs...)
	})
}

// Exec 执行一条语句，出错则让测试失败。
func (f *Fixture) Exec(t TB, sql string, args ...any) {
	t.Helper()
	if _, err := f.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v\nSQL: %s", err, strings.TrimSpace(sql))
	}
}

// Cleanup 在测试结束时执行一条语句，并按拆卸一贯该有的方式忽略其错误：失败的测试
// 通常已把行留在拆卸够不着的状态，把那个错误叠在真正的失败之上只会掩盖它。
func (f *Fixture) Cleanup(t TB, sql string, args ...any) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = f.Pool.Exec(context.Background(), sql, args...)
	})
}

// Row 是一条待执行的单行查询。
type Row struct {
	f    *Fixture
	t    TB
	sql  string
	args []any
}

// QueryRow 准备一条单行查询，其 Scan 出错时会让测试失败。
func (f *Fixture) QueryRow(t TB, sql string, args ...any) *Row {
	t.Helper()
	return &Row{f: f, t: t, sql: sql, args: args}
}

// Scan 把行读到 dest，查询出错或没匹配到任何行时让测试失败。
func (r *Row) Scan(dest ...any) {
	r.t.Helper()
	if err := r.f.Pool.QueryRow(context.Background(), r.sql, r.args...).Scan(dest...); err != nil {
		r.t.Fatalf("query: %v\nSQL: %s", err, strings.TrimSpace(r.sql))
	}
}

// Count 返回 `SELECT count(*) ...` 查询产出的计数。
func (f *Fixture) Count(t TB, sql string, args ...any) int {
	t.Helper()
	var n int
	f.QueryRow(t, sql, args...).Scan(&n)
	return n
}

// ---- 类型化 builder ----------------------------------------------
//
// 每个都填好一行"没有就无法插入"的列，并接受测试真正关心的列作为可选覆盖。
// 默认值刻意朴素：依赖某个默认值的测试必须自己点名它。

// User 插入一个用户行。
func (f *Fixture) User(t TB, name, email string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "user", merge(Cols{
		"name":  name,
		"email": email,
	}, over))
}

// Workspace 插入一个工作区行。
func (f *Fixture) Workspace(t TB, name, slug string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "workspace", merge(Cols{
		"name":         name,
		"slug":         slug,
		"description":  "",
		"issue_prefix": "",
	}, over))
}

// Member 把用户加入工作区。
func (f *Fixture) Member(t TB, workspaceID, userID, role string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "member", merge(Cols{
		"workspace_id": workspaceID,
		"user_id":      userID,
		"role":         role,
	}, over))
}

// Issue 插入一条属于夹具工作区与用户的 issue。
//
// number 从该工作区已有 issue 推导而不是写死，让同一测试建的两条 issue 不会在
// 工作区的 issue 编号上冲突。
func (f *Fixture) Issue(t TB, title string, over ...Cols) string {
	t.Helper()
	cols := merge(Cols{
		"workspace_id": f.WorkspaceID,
		"title":        title,
		"status":       "todo",
		"priority":     "none",
		"creator_type": "member",
		"creator_id":   f.UserID,
		"position":     0,
	}, over)
	if _, ok := cols["number"]; !ok {
		workspaceID, _ := cols["workspace_id"].(string)
		cols["number"] = Raw(fmt.Sprintf(
			"(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = %s)",
			quoteLiteral(workspaceID)))
	}
	return f.Insert(t, "issue", cols)
}

// Comment 在 issueID 下插入一条评论。
func (f *Fixture) Comment(t TB, issueID, content string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "comment", merge(Cols{
		"issue_id":     issueID,
		"workspace_id": f.WorkspaceID,
		"author_type":  "member",
		"author_id":    f.UserID,
		"content":      content,
		"type":         "comment",
	}, over))
}

// Runtime 插入一个在线的云 runtime。
func (f *Fixture) Runtime(t TB, name string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "agent_runtime", merge(Cols{
		"workspace_id": f.WorkspaceID,
		"daemon_id":    nil,
		"name":         name,
		"runtime_mode": "cloud",
		"provider":     "handler_test_runtime",
		"status":       "online",
		"device_info":  "",
		"metadata":     Raw("'{}'::jsonb"),
		"last_seen_at": Raw("now()"),
		"visibility":   "private",
		"owner_id":     f.UserID,
	}, over))
}

// Agent 插入一个绑定 runtimeID 的 agent；传空 runtimeID 表示没有 runtime 的 agent。
func (f *Fixture) Agent(t TB, name, runtimeID string, over ...Cols) string {
	t.Helper()
	cols := merge(Cols{
		"workspace_id":         f.WorkspaceID,
		"name":                 name,
		"description":          "",
		"runtime_mode":         "cloud",
		"runtime_config":       Raw("'{}'::jsonb"),
		"visibility":           "private",
		"permission_mode":      "private",
		"max_concurrent_tasks": 1,
		"owner_id":             f.UserID,
	}, over)
	if _, ok := cols["runtime_id"]; !ok && runtimeID != "" {
		cols["runtime_id"] = runtimeID
	}
	return f.Insert(t, "agent", cols)
}

// Task 为 agentID 排入一条任务。
func (f *Fixture) Task(t TB, agentID string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "agent_task_queue", merge(Cols{
		"agent_id": agentID,
		"status":   "queued",
		"priority": 0,
	}, over))
}

// Squad 插入一个由 leaderID 带领的 squad。
func (f *Fixture) Squad(t TB, name, leaderID string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "squad", merge(Cols{
		"workspace_id": f.WorkspaceID,
		"name":         name,
		"description":  "",
		"leader_id":    leaderID,
		"creator_id":   f.UserID,
	}, over))
}

// SquadMember 把 agent 或 member 加入 squad。
func (f *Fixture) SquadMember(t TB, squadID, memberType, memberID string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "squad_member", merge(Cols{
		"squad_id":    squadID,
		"member_type": memberType,
		"member_id":   memberID,
	}, over))
}

// Project 插入一个项目。
func (f *Fixture) Project(t TB, title string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "project", merge(Cols{
		"workspace_id": f.WorkspaceID,
		"title":        title,
		"status":       "planned",
		"priority":     "none",
	}, over))
}

// ChatSession 插入一个带 agentID 的聊天会话。
func (f *Fixture) ChatSession(t TB, agentID string, over ...Cols) string {
	t.Helper()
	return f.Insert(t, "chat_session", merge(Cols{
		"workspace_id": f.WorkspaceID,
		"agent_id":     agentID,
		"creator_id":   f.UserID,
		"title":        "",
		"status":       "active",
	}, over))
}

// ---- 内部实现 ------------------------------------------------------------

// deleteOnCleanup 给带 id 的行注册按 id 删除的清理。
func (f *Fixture) deleteOnCleanup(t TB, table, id string) {
	t.Cleanup(func() {
		_, _ = f.Pool.Exec(context.Background(),
			fmt.Sprintf("DELETE FROM %s WHERE id = $1", quoteIdent(table)), id)
	})
}

// compile 把列映射转成列名、值占位符与绑定参数。列排序使生成的 SQL 稳定，失败的
// 语句更易读、更易在两次运行间做 diff。
func compile(cols Cols) (names []string, placeholders []string, args []any) {
	names = make([]string, 0, len(cols))
	for name := range cols {
		names = append(names, name)
	}
	sort.Strings(names)

	placeholders = make([]string, 0, len(names))
	args = make([]any, 0, len(names))
	for _, name := range names {
		if raw, ok := cols[name].(Raw); ok {
			placeholders = append(placeholders, string(raw))
			continue
		}
		args = append(args, cols[name])
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	for i, name := range names {
		names[i] = quoteIdent(name)
	}
	return names, placeholders, args
}

// merge 把覆盖列叠加到基础列上（后者胜出）。
func merge(base Cols, over []Cols) Cols {
	out := make(Cols, len(base))
	for k, v := range base {
		out[k] = v
	}
	for _, o := range over {
		for k, v := range o {
			out[k] = v
		}
	}
	return out
}

// quoteIdent 给标识符加双引号，让保留字——`user` 这个表名就是——也能用作表/列名。
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteLiteral 给字符串字面量加单引号并转义其中的单引号。
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
