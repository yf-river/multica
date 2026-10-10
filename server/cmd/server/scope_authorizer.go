// scope_authorizer.go
//
// 职责：为实时推送订阅（realtime hub）实现"作用域级"鉴权。
// 实时通道里的 workspace / user 级作用域由 hub 根据连接身份自己校验，但
// task / chat 这类"细粒度资源"作用域必须回查数据库确认：资源存在、属于
// 调用者所在工作区、且（对 chat 资源）是该调用者本人创建的。本文件把
// 这些查库逻辑包成 realtime.ScopeAuthorizer 的实现，订阅方在放行连接前
// 调用它做最后一道把关。
package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// scopeAuthQuerier 是 db.Queries 中 scope 鉴权真正用到的那一小部分方法。
// 声明成接口，就可以在单元测试里用内存假实现替换数据库（不需要真连库）。
type scopeAuthQuerier interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error)
	GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error)
}

// dbScopeAuthorizer 为 task / chat 两种作用域实现 realtime.ScopeAuthorizer
// （workspace/user 作用域由 hub 拿连接身份自行校验）。只有满足以下条件才
// 放行：资源真实存在、属于调用者的工作区，且对 chat 资源还得是调用者创建
// 的（与 HTTP 层"仅创建者可访问"的模型保持一致）。
type dbScopeAuthorizer struct{ q scopeAuthQuerier }

func newScopeAuthorizer(q scopeAuthQuerier) *dbScopeAuthorizer { return &dbScopeAuthorizer{q: q} }

// scopeLookupErr 把"查作用域资源"的报错翻译成鉴权结果。资源不存在
// （pgx.ErrNoRows）是一次正当的拒绝——HTTP 层对 not-found 回 404 而非 403，
// 所以实时层把它报告成普通的"forbidden"拒绝。其他错误（连接池耗尽、context
// 取消、网络抖动）是临时性查询失败，必须向上传播，让 handleSubscribe 上报
// "lookup_failed"，而不能把数据库故障伪装成一片权限拒绝把问题掩盖掉。
func scopeLookupErr(err error) (bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, err
}

// AuthorizeScope 判断某用户能否订阅指定 scope。入参非法（工作区/资源 ID 空或
// 不是合法 UUID）直接拒绝；支持的作用域见 realtime.ScopeTask / ScopeChat，
// 其他一律拒绝。
func (a *dbScopeAuthorizer) AuthorizeScope(ctx context.Context, userID, workspaceID, scopeType, scopeID string) (bool, error) {
	if workspaceID == "" || scopeID == "" {
		return false, nil
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, nil
	}
	idUUID, err := util.ParseUUID(scopeID)
	if err != nil {
		return false, nil
	}
	switch scopeType {
	case realtime.ScopeTask:
		task, err := a.q.GetAgentTask(ctx, idUUID)
		if err != nil {
			return scopeLookupErr(err)
		}
		// Issue 类任务：对工作区任意成员可见，因此只需确认它挂在调用者的
		// 工作区下。
		if task.IssueID.Valid {
			issue, err := a.q.GetIssue(ctx, task.IssueID)
			if err != nil {
				return scopeLookupErr(err)
			}
			if issue.OriginType.Valid && issue.OriginType.String == "health_report" {
				uid, err := util.ParseUUID(userID)
				if err != nil || issue.CreatorType != "member" || issue.CreatorID != uid {
					return false, nil
				}
			}
			return issue.WorkspaceID == wsUUID, nil

		}
		// Chat 类任务：只有会话创建者本人能订阅，镜像 HTTP 层对 chat 资源
		// 的"仅创建者可访问"限制。
		if task.ChatSessionID.Valid {
			sess, err := a.q.GetChatSession(ctx, task.ChatSessionID)
			if err != nil {
				return scopeLookupErr(err)
			}
			if sess.WorkspaceID != wsUUID {
				return false, nil
			}
			uidUUID, err := util.ParseUUID(userID)
			if err != nil || sess.CreatorID != uidUUID {
				return false, nil
			}
			return true, nil
		}
		return false, nil
	case realtime.ScopeChat:
		sess, err := a.q.GetChatSession(ctx, idUUID)
		if err != nil {
			return scopeLookupErr(err)
		}
		if sess.WorkspaceID != wsUUID {
			return false, nil
		}
		// Chat sessions are private to their creator (see handler/chat.go:
		// GetChatSession / SendChatMessage / MarkChatSessionRead all enforce
		// CreatorID == userID). The realtime layer must not weaken this:
		// otherwise any workspace member who learns a session_id could
		// subscribe to chat:message / chat:done / chat:session_read for a
		// peer's private chat.
		//
		// 中文说明：chat 会话对其创建者是私有的（见 handler/chat.go：GetChatSession、
		// SendChatMessage、MarkChatSessionRead 全都强制 CreatorID == userID）。
		// 实时层绝不能放松这条：否则任何工作区成员只要打听到别人的 session_id，
		// 就能订阅对方的 chat:message / chat:done / chat:session_read 私聊通道。
		uidUUID, err := util.ParseUUID(userID)
		if err != nil || sess.CreatorID != uidUUID {
			return false, nil
		}
		return true, nil
	default:
		return false, nil
	}
}
