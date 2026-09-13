// source_context.go —— 「来源上下文」的构建与持久化：把某条 issue/评论线程的只读历史快照（评论、
// 附件、作者、描述等）固定下来，供 quick-create / 手动「基于评论建 issue」时给 agent 做背景材料。
// 职责：① 构建不可变快照并计算内容摘要（digest），用于预览与创建之间的「来源未变」校验；
// ② 对象级附件克隆（把原附件 URL 改写为克隆 URL），③ 快照与克隆附件的持久化，④ 孤儿克隆/过期
// 上下文的清理。文件里的数据限制与模式版本常量都导出让调用方复用。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const (
	// SourceContextVersion 是快照结构的当前 schema 版本，渲染/agent 侧按它做兼容。
	SourceContextVersion = int16(1)
	// SourceContextUsage 标识该上下文的用途：只读的历史背景信息。
	SourceContextUsage       = "read_only_historical_background"
	SourceContextMaxComments = 256
	// SourceContextMaxTextBytes 是通用/手动捕获的文本字节上限，沿用既有口径。
	SourceContextMaxTextBytes = 1 << 20
	// SourceContextMaxAgentSnapshotBytes 更小：agent 模式会把不可变快照原文引到它的第一个用户
	// prompt 里。
	SourceContextMaxAgentSnapshotBytes = 64 << 10
	// SourceContextMaxAgentInputBytes 覆盖「快照 + 新指令」的总预算。分词器产出的 token 数不可能
	// 超过输入的字节数；把动态输入压在 72 KiB 以下，才能给稳定的 quick-create 外壳、runtime 简报、
	// 系统 prompt 与工具留出 10 万+ token 的空间。
	SourceContextMaxAgentInputBytes  = 72 << 10
	SourceContextMaxAgentPromptBytes = 96 << 10
	SourceContextMaxAttachments      = 100
	SourceContextMaxAttachmentBytes  = int64(500 << 20)
)

const (
	// sourceContextObjectDeleteTimeout 限制单次对象存储删除的时长。清理跑在独立 sweeper goroutine
	// 上，不健康的存储端点因此无法再饿死 runtime/task 恢复；但没有单对象超时的话，一次卡死的删除
	// 仍会拖住整轮清理。
	sourceContextObjectDeleteTimeout = 30 * time.Second
	// sourceContextIntentReleaseTimeout 限制删除失败后的重试退避写入。它刻意脱离调用方的轮次预算：
	// 当正是预算把删除截断时，一次被取消的 release 会让 intent 立刻可被认领，之后每一轮都会继续
	// 优先重试同一个不健康对象，而把排在后面的对象晾着。
	sourceContextIntentReleaseTimeout = 5 * time.Second
)

var (
	// 下面这组哨兵错误贯穿「捕获 → 校验 → 创建」链路：handler 靠 errors.Is 区分各类冲突场景，
	// 再翻译成对应的 HTTP 状态或提示文案。
	ErrAnchorCommentDeleted          = errors.New("anchor comment deleted")
	ErrSourceIssueDeleted            = errors.New("source issue deleted")
	ErrSourceContextChanged          = errors.New("source context changed")
	ErrSourceContextInvalid          = errors.New("source context invalid comment thread")
	ErrSourceContextTooLarge         = errors.New("source context too large")
	ErrSourceContextRetryUnavailable = errors.New("source context retry unavailable")
)

// validateSourceContextAgentBytes 校验快照字节数与总输入是否都落在 agent 模式预算内。
func validateSourceContextAgentBytes(snapshotBytes int, prompt string) error {
	if snapshotBytes < 0 || snapshotBytes > SourceContextMaxAgentSnapshotBytes || snapshotBytes+len(prompt) > SourceContextMaxAgentInputBytes {
		return ErrSourceContextTooLarge
	}
	return nil
}

// ValidateSourceContextAgentInput 保证每个被接受的 source-context quick-create 都在 daemon 回归测试
// 锁定的 prompt 预算内（以它完整的用户消息外壳计）。它先投影「将要持久化的快照」——含捕获元数据、
// 克隆附件 id、改写后的 URL——预览 payload 因此不会低估 daemon 真正会引用的体积。本函数在对象克隆
// 与任务入队之前运行，无外部副作用。
func ValidateSourceContextAgentInput(build SourceContextBuild, capturedBy pgtype.UUID, prompt string) error {
	projectedCloneID := pgtype.UUID{Valid: true}
	clones := make([]SourceContextClone, len(build.Rows))
	for i := range clones {
		clones[i].ID = projectedCloneID
	}
	// RFC3339Nano 在纳秒非零时输出最长表示——用它当最坏情况来估。
	projectedCapturedAt := time.Date(2000, time.January, 1, 0, 0, 0, 999999999, time.UTC)
	projected, err := PrepareSourceContextCapture(
		build,
		pgtype.UUID{Valid: true},
		pgtype.UUID{Valid: true},
		capturedBy,
		projectedCapturedAt,
		clones,
	)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(projected.Snapshot)
	if err != nil {
		return fmt.Errorf("marshal projected source context: %w", err)
	}
	return validateSourceContextAgentBytes(len(payload), prompt)
}

// SourceContextLimitUsage 是构建后统计出的快照规模：评论条数、文本字节、附件个数与字节总和，
// 用作文档化上限（SourceContextMax*）的判定输入。
type SourceContextLimitUsage struct {
	CommentCount    int   `json:"comment_count"`
	TextBytes       int   `json:"text_bytes"`
	AttachmentCount int   `json:"attachment_count"`
	AttachmentBytes int64 `json:"attachment_bytes"`
}

// SourceContextAttachment 是快照里附件行的 JSON 形态。SourceAttachmentID 仅在克隆改写后填原附件 id。
type SourceContextAttachment struct {
	ID                 string `json:"id"`
	SourceAttachmentID string `json:"source_attachment_id,omitempty"`
	OwnerType          string `json:"owner_type"`
	OwnerID            string `json:"owner_id"`
	Filename           string `json:"filename"`
	ContentType        string `json:"content_type"`
	SizeBytes          int64  `json:"size_bytes"`
	CreatedAt          string `json:"created_at"`
}

// SourceContextAuthor 是评论作者的 JSON 形态（member/agent + id + 展示名）。
type SourceContextAuthor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SourceContextIssueSnapshot 是来源 issue 的只读快照，不含评论线程。
type SourceContextIssueSnapshot struct {
	ID          string                    `json:"id"`
	Identifier  string                    `json:"identifier"`
	Number      int32                     `json:"number"`
	Title       string                    `json:"title"`
	Description *string                   `json:"description"`
	CreatedAt   string                    `json:"created_at"`
	UpdatedAt   string                    `json:"updated_at"`
	Revision    int64                     `json:"revision"`
	Attachments []SourceContextAttachment `json:"attachments"`
}

// SourceContextCommentSnapshot 是线程里单条评论的只读快照。ParentID 为空表示线程根。
type SourceContextCommentSnapshot struct {
	ID          string                    `json:"id"`
	ParentID    *string                   `json:"parent_id"`
	Type        string                    `json:"type"`
	Content     string                    `json:"content"`
	Author      SourceContextAuthor       `json:"author"`
	CreatedAt   string                    `json:"created_at"`
	UpdatedAt   string                    `json:"updated_at"`
	Revision    int64                     `json:"revision"`
	Attachments []SourceContextAttachment `json:"attachments"`
}

// SourceContextSnapshot 是完整捕获的不可变 JSON 快照：来源 issue + 锚点评论所在线程 + 元数据。
type SourceContextSnapshot struct {
	Version          int16                          `json:"version,omitempty"`
	CapturedByUserID string                         `json:"captured_by_user_id,omitempty"`
	CapturedAt       string                         `json:"captured_at,omitempty"`
	SourceIssue      SourceContextIssueSnapshot     `json:"source_issue"`
	CommentThread    []SourceContextCommentSnapshot `json:"comment_thread"`
	AnchorCommentID  string                         `json:"anchor_comment_id"`
}

// SourceContextBuild 是一次「构建」的产物：快照 + 内容摘要 + 校验 token + 规模统计 + 原附件行。
type SourceContextBuild struct {
	Snapshot SourceContextSnapshot
	Digest   string
	Token    string
	Limits   SourceContextLimitUsage
	Rows     []db.Attachment
}

// SourceContextClone 描述一次附件克隆（从原附件对象复制出的新对象，持有新 id/URL）。
type SourceContextClone struct {
	ID          pgtype.UUID
	Filename    string
	URL         string
	ContentType string
	SizeBytes   int64
}

// SourceContextCapture 是准备好落库的捕获：上下文行 + 把原附件 id 改写为克隆 id 后的快照 + 克隆清单。
type SourceContextCapture struct {
	ID               pgtype.UUID
	WorkspaceID      pgtype.UUID
	SourceIssueID    pgtype.UUID
	AnchorCommentID  pgtype.UUID
	CapturedByUserID pgtype.UUID
	CapturedAt       time.Time
	Snapshot         SourceContextSnapshot
	Digest           string
	Clones           []SourceContextClone
}

// sourceContextTime 把 pgtype.Timestamptz 归一化成 RFC3339Nano 的 UTC 字符串，供快照 JSON 使用。
func sourceContextTime(v pgtype.Timestamptz) string {
	return v.Time.UTC().Format(time.RFC3339Nano)
}

// sourceContextText 把 pgtype.Text 转成 *string（无效值 → nil），描述类可空字段用。
func sourceContextText(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// sourceContextAuthorName 解析 author 的展示名：member 查用户，agent 查工作区内 agent。
func sourceContextAuthorName(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, authorType string, authorID pgtype.UUID) (string, error) {
	switch authorType {
	case "member":
		user, err := q.GetUser(ctx, authorID)
		if err != nil {
			return "", fmt.Errorf("resolve source-context member author: %w", err)
		}
		return user.Name, nil
	case "agent":
		agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: authorID, WorkspaceID: workspaceID})
		if err != nil {
			return "", fmt.Errorf("resolve source-context agent author: %w", err)
		}
		return agent.Name, nil
	}
	return "", fmt.Errorf("unsupported source-context author type %q", authorType)
}

// attachmentSnapshot 把 DB 附件行转成快照里的 JSON 形态。
func attachmentSnapshot(row db.Attachment, ownerType, ownerID string) SourceContextAttachment {
	return SourceContextAttachment{
		ID:          util.UUIDToString(row.ID),
		OwnerType:   ownerType,
		OwnerID:     ownerID,
		Filename:    row.Filename,
		ContentType: row.ContentType,
		SizeBytes:   row.SizeBytes,
		CreatedAt:   sourceContextTime(row.CreatedAt),
	}
}

// sourceContextDigest 计算快照的规范内容摘要（sha256）。版本、捕获人、捕获时刻与变更元数据（更新时间、
// revision）被清零——它们不属于「来源内容」的比对范畴；这样同一段来源无论何时捕获、由谁捕获，摘要
// 都稳定，预览 token 才能在创建时再次对上。
func sourceContextDigest(snapshot SourceContextSnapshot) (string, error) {
	canonical := snapshot
	// CommentThread 是切片，只复制外层结构的话会与「将被持久化的那个 snapshot」共享底层数组；
	// 摘要投影绝不能把捕获元数据从那个 snapshot 里擦掉。
	canonical.CommentThread = append([]SourceContextCommentSnapshot(nil), snapshot.CommentThread...)
	canonical.Version = 0
	canonical.CapturedByUserID = ""
	canonical.CapturedAt = ""
	canonical.SourceIssue.UpdatedAt = ""
	canonical.SourceIssue.Revision = 0
	for i := range canonical.CommentThread {
		// 显示名是活的身份元数据，不是来源内容。预览与创建之间的一次改名不应让一份本就相同的捕获
		// 失效；改名由详情渲染独立报告。
		canonical.CommentThread[i].Author.Name = ""
		canonical.CommentThread[i].UpdatedAt = ""
		canonical.CommentThread[i].Revision = 0
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal canonical source context: %w", err)
	}
	digestBytes := sha256.Sum256(payload)
	return hex.EncodeToString(digestBytes[:]), nil
}

// BuildSourceIssueSnapshot 构建快照中「issue 所有」的部分，不要求锚点评论仍存在。来源评论（或其某个
// 祖先）已被删除时，详情渲染用它在删除后仍能准确上报 source_issue_state。
func BuildSourceIssueSnapshot(ctx context.Context, q *db.Queries, workspaceID, issueID pgtype.UUID) (SourceContextIssueSnapshot, []db.Attachment, error) {
	issue, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceContextIssueSnapshot{}, nil, ErrSourceIssueDeleted
	}
	if err != nil {
		return SourceContextIssueSnapshot{}, nil, fmt.Errorf("load source issue: %w", err)
	}
	workspace, err := q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return SourceContextIssueSnapshot{}, nil, fmt.Errorf("load source workspace: %w", err)
	}
	attachments, err := q.ListSourceContextIssueAttachments(ctx, db.ListSourceContextIssueAttachmentsParams{IssueID: issue.ID, WorkspaceID: workspaceID})
	if err != nil {
		return SourceContextIssueSnapshot{}, nil, fmt.Errorf("load issue attachments: %w", err)
	}
	sort.Slice(attachments, func(i, j int) bool {
		return util.UUIDToString(attachments[i].ID) < util.UUIDToString(attachments[j].ID)
	})
	attachmentSnapshots := make([]SourceContextAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		attachmentSnapshots = append(attachmentSnapshots, attachmentSnapshot(attachment, "issue", util.UUIDToString(issue.ID)))
	}
	return SourceContextIssueSnapshot{
		ID: util.UUIDToString(issue.ID), Identifier: workspace.IssuePrefix + "-" + fmt.Sprint(issue.Number), Number: issue.Number,
		Title: issue.Title, Description: sourceContextText(issue.Description),
		CreatedAt: sourceContextTime(issue.CreatedAt), UpdatedAt: sourceContextTime(issue.UpdatedAt), Revision: issue.Revision,
		Attachments: attachmentSnapshots,
	}, attachments, nil
}

// buildSourceContextCommentSnapshots 把评论线程历史行连同各评论附件打包成快照数组，附件按 id 排序、
// 按评论分组保证确定性输出。
func buildSourceContextCommentSnapshots(
	ctx context.Context,
	q *db.Queries,
	workspaceID pgtype.UUID,
	issueID pgtype.UUID,
	historyRows []db.ListCommentThreadHistoryRow,
) ([]SourceContextCommentSnapshot, []db.Attachment, error) {
	commentIDs := make([]pgtype.UUID, 0, len(historyRows))
	for _, row := range historyRows {
		commentIDs = append(commentIDs, row.ID)
	}
	commentAttachments := make([]db.Attachment, 0)
	if len(commentIDs) > 0 {
		var err error
		commentAttachments, err = q.ListSourceContextCommentAttachments(ctx, db.ListSourceContextCommentAttachmentsParams{
			WorkspaceID: workspaceID, IssueID: issueID, CommentIds: commentIDs,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("load comment attachments: %w", err)
		}
	}
	sort.Slice(commentAttachments, func(i, j int) bool {
		return util.UUIDToString(commentAttachments[i].ID) < util.UUIDToString(commentAttachments[j].ID)
	})

	byComment := make(map[string][]SourceContextAttachment)
	for _, attachment := range commentAttachments {
		commentID := util.UUIDToString(attachment.CommentID)
		byComment[commentID] = append(byComment[commentID], attachmentSnapshot(attachment, "comment", commentID))
	}
	commentThread := make([]SourceContextCommentSnapshot, 0, len(historyRows))
	for _, row := range historyRows {
		commentID := util.UUIDToString(row.ID)
		authorName, err := sourceContextAuthorName(ctx, q, workspaceID, row.AuthorType, row.AuthorID)
		if err != nil {
			return nil, nil, err
		}
		var parentID *string
		if row.ParentID.Valid {
			value := util.UUIDToString(row.ParentID)
			parentID = &value
		}
		attachments := byComment[commentID]
		if attachments == nil {
			attachments = []SourceContextAttachment{}
		}
		commentThread = append(commentThread, SourceContextCommentSnapshot{
			ID: commentID, ParentID: parentID, Type: row.Type, Content: row.Content,
			Author:    SourceContextAuthor{Type: row.AuthorType, ID: util.UUIDToString(row.AuthorID), Name: authorName},
			CreatedAt: sourceContextTime(row.CreatedAt), UpdatedAt: sourceContextTime(row.UpdatedAt), Revision: row.Revision,
			Attachments: attachments,
		})
	}
	return commentThread, commentAttachments, nil
}

// BuildSourceContextThreadAtCaptureBoundary 在锚点评论已不存在时，按「捕获时的边界」重建线程中仍
// 存活的部分。用不可变根与锚点时间戳保住原有的时间边界，让详情响应能分类「每个被捕获节点」的变化，
// 而不是只报一条被删的锚点。
func BuildSourceContextThreadAtCaptureBoundary(
	ctx context.Context,
	q *db.Queries,
	workspaceID pgtype.UUID,
	captured SourceContextSnapshot,
) ([]SourceContextCommentSnapshot, error) {
	if len(captured.CommentThread) == 0 || captured.CommentThread[0].ParentID != nil {
		return nil, ErrSourceContextInvalid
	}
	var anchor *SourceContextCommentSnapshot
	for i := range captured.CommentThread {
		if captured.CommentThread[i].ID == captured.AnchorCommentID {
			anchor = &captured.CommentThread[i]
			break
		}
	}
	if anchor == nil {
		return nil, ErrSourceContextInvalid
	}
	rootID, err := util.ParseUUID(captured.CommentThread[0].ID)
	if err != nil {
		return nil, fmt.Errorf("parse captured thread root: %w", err)
	}
	issueID, err := util.ParseUUID(captured.SourceIssue.ID)
	if err != nil {
		return nil, fmt.Errorf("parse captured source issue: %w", err)
	}
	anchorID, err := util.ParseUUID(captured.AnchorCommentID)
	if err != nil {
		return nil, fmt.Errorf("parse captured anchor: %w", err)
	}
	anchorCreatedAt, err := time.Parse(time.RFC3339Nano, anchor.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse captured anchor timestamp: %w", err)
	}
	historyRows, err := q.ListCommentThreadHistory(ctx, db.ListCommentThreadHistoryParams{
		RootID: rootID, WorkspaceID: workspaceID, IssueID: issueID,
		AnchorCreatedAt: pgtype.Timestamptz{Time: anchorCreatedAt, Valid: true}, AnchorID: anchorID,
		RowLimit: SourceContextMaxComments + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("load source thread at capture boundary: %w", err)
	}
	commentThread, _, err := buildSourceContextCommentSnapshots(ctx, q, workspaceID, issueID, historyRows)
	return commentThread, err
}

// BuildSourceContext 以某条锚点评论为基准构建一次来源上下文：快照 + 摘要 + token + 规模统计。
// 它校验锚点存在且是普通评论、祖先链路完整无环且封顶、线程内容非空且与祖先路径两端一致，超出
// SourceContextMax* 上限即返回 ErrSourceContextTooLarge。token 形如 "sha256:<digest>:<issueID>"，
// 供预览后创建时做来源未变校验。
func BuildSourceContext(ctx context.Context, q *db.Queries, workspaceID, anchorCommentID pgtype.UUID) (SourceContextBuild, error) {
	anchor, err := q.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: anchorCommentID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceContextBuild{}, ErrAnchorCommentDeleted
	}
	if err != nil {
		return SourceContextBuild{}, fmt.Errorf("load anchor comment: %w", err)
	}
	if anchor.Type != "comment" {
		return SourceContextBuild{}, ErrSourceContextInvalid
	}
	issueSnapshot, issueAttachments, err := BuildSourceIssueSnapshot(ctx, q, workspaceID, anchor.IssueID)
	if err != nil {
		return SourceContextBuild{}, err
	}
	ancestorRows, err := q.ListCommentAncestorPath(ctx, db.ListCommentAncestorPathParams{
		CommentID: anchorCommentID, WorkspaceID: workspaceID, IssueID: anchor.IssueID,
	})
	if err != nil {
		return SourceContextBuild{}, fmt.Errorf("load source path: %w", err)
	}
	if len(ancestorRows) == 0 || ancestorRows[len(ancestorRows)-1].ID != anchorCommentID || ancestorRows[0].ParentID.Valid {
		return SourceContextBuild{}, ErrSourceContextInvalid
	}
	if len(ancestorRows) > SourceContextMaxComments {
		return SourceContextBuild{Limits: SourceContextLimitUsage{CommentCount: len(ancestorRows)}}, ErrSourceContextTooLarge
	}
	seenAncestors := make(map[string]struct{}, len(ancestorRows))
	for _, row := range ancestorRows {
		id := util.UUIDToString(row.ID)
		if row.Cycle {
			return SourceContextBuild{}, ErrSourceContextInvalid
		}
		if _, exists := seenAncestors[id]; exists {
			return SourceContextBuild{}, ErrSourceContextInvalid
		}
		seenAncestors[id] = struct{}{}
	}

	historyRows, err := q.ListCommentThreadHistory(ctx, db.ListCommentThreadHistoryParams{
		RootID:          ancestorRows[0].ID,
		WorkspaceID:     workspaceID,
		IssueID:         anchor.IssueID,
		AnchorCreatedAt: anchor.CreatedAt,
		AnchorID:        anchorCommentID,
		RowLimit:        SourceContextMaxComments + 1,
	})
	if err != nil {
		return SourceContextBuild{}, fmt.Errorf("load source thread history: %w", err)
	}
	if len(historyRows) == 0 || historyRows[0].ID != ancestorRows[0].ID || historyRows[len(historyRows)-1].ID != anchorCommentID {
		return SourceContextBuild{}, ErrSourceContextInvalid
	}
	if len(historyRows) > SourceContextMaxComments {
		return SourceContextBuild{Limits: SourceContextLimitUsage{CommentCount: len(historyRows)}}, ErrSourceContextTooLarge
	}
	seenHistory := make(map[string]struct{}, len(historyRows))
	for _, row := range historyRows {
		id := util.UUIDToString(row.ID)
		if _, exists := seenHistory[id]; exists {
			return SourceContextBuild{}, ErrSourceContextInvalid
		}
		seenHistory[id] = struct{}{}
	}

	commentThread, commentAttachments, err := buildSourceContextCommentSnapshots(ctx, q, workspaceID, anchor.IssueID, historyRows)
	if err != nil {
		return SourceContextBuild{}, err
	}

	allRows := make([]db.Attachment, 0, len(issueAttachments)+len(commentAttachments))
	for _, attachment := range issueAttachments {
		allRows = append(allRows, attachment)
	}
	for _, attachment := range commentAttachments {
		allRows = append(allRows, attachment)
	}
	snapshot := SourceContextSnapshot{
		SourceIssue:     issueSnapshot,
		CommentThread:   commentThread,
		AnchorCommentID: util.UUIDToString(anchorCommentID),
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return SourceContextBuild{}, fmt.Errorf("marshal source context: %w", err)
	}
	limits := SourceContextLimitUsage{CommentCount: len(commentThread), TextBytes: len(payload), AttachmentCount: len(allRows)}
	for _, row := range allRows {
		limits.AttachmentBytes += row.SizeBytes
	}
	if limits.TextBytes > SourceContextMaxTextBytes || limits.AttachmentCount > SourceContextMaxAttachments || limits.AttachmentBytes > SourceContextMaxAttachmentBytes {
		return SourceContextBuild{Snapshot: snapshot, Limits: limits}, ErrSourceContextTooLarge
	}
	// revision 与更新时间是有用的捕获元数据，但不属于规范内容比对。一段被改了又逐字节还原的来源，
	// 必须再次匹配上预览 token。
	digest, err := sourceContextDigest(snapshot)
	if err != nil {
		return SourceContextBuild{}, err
	}
	return SourceContextBuild{
		Snapshot: snapshot,
		Digest:   digest,
		Token:    "sha256:" + digest + ":" + util.UUIDToString(anchor.IssueID),
		Limits:   limits,
		Rows:     allRows,
	}, nil
}

// ParseSourceContextToken 解析校验 token，拆出 digest 与来源 issue id；格式不对返回 ok=false。
func ParseSourceContextToken(token string) (digest string, sourceIssueID pgtype.UUID, ok bool) {
	parts := strings.Split(token, ":")
	if len(parts) != 3 || parts[0] != "sha256" || len(parts[1]) != sha256.Size*2 {
		return "", pgtype.UUID{}, false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return "", pgtype.UUID{}, false
	}
	id, err := util.ParseUUID(parts[2])
	if err != nil {
		return "", pgtype.UUID{}, false
	}
	return parts[1], id, true
}

// PrepareSourceContextCapture 把一次构建升级为「可落库的捕获」：把快照里的原附件 id/URL 改写成克隆
// 对象，并盖上版本与捕获元数据。clones 必须与 build.Rows 一一对应。
func PrepareSourceContextCapture(build SourceContextBuild, contextID, workspaceID, capturedBy pgtype.UUID, capturedAt time.Time, clones []SourceContextClone) (SourceContextCapture, error) {
	if len(clones) != len(build.Rows) {
		return SourceContextCapture{}, fmt.Errorf("source context clone count mismatch")
	}
	// 存储克隆运行时，预览构建仍可被调用方缓存。先深拷贝再替换原附件 id 为克隆 id，保证预览/token
	// 输入保持不可变。
	snapshotJSON, err := json.Marshal(build.Snapshot)
	if err != nil {
		return SourceContextCapture{}, fmt.Errorf("copy source context snapshot: %w", err)
	}
	var snapshot SourceContextSnapshot
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		return SourceContextCapture{}, fmt.Errorf("copy source context snapshot: %w", err)
	}
	cloneBySource := make(map[string]string, len(clones))
	replacements := make([]string, 0, len(clones)*2)
	for i, row := range build.Rows {
		sourceID := util.UUIDToString(row.ID)
		cloneID := util.UUIDToString(clones[i].ID)
		sourceDownloadPath := util.AttachmentDownloadPath(sourceID)
		cloneDownloadPath := util.AttachmentDownloadPath(cloneID)
		cloneBySource[sourceID] = cloneID
		replacements = append(
			replacements,
			sourceDownloadPath,
			cloneDownloadPath,
		)
		// 公共存储部署会把附件对象的 URL 直接持久化。它和稳定 API 路由一样属于「来源所有」，必须
		// 在原对象删除后仍能经克隆解析到。
		if row.Url != "" && row.Url != sourceDownloadPath {
			replacements = append(replacements, row.Url, cloneDownloadPath)
		}
	}
	replacer := strings.NewReplacer(replacements...)
	rewriteContent := func(content string) string {
		return replacer.Replace(content)
	}
	rewrite := func(items []SourceContextAttachment) {
		for i := range items {
			sourceID := items[i].ID
			items[i].SourceAttachmentID = sourceID
			items[i].ID = cloneBySource[sourceID]
		}
	}
	rewrite(snapshot.SourceIssue.Attachments)
	if snapshot.SourceIssue.Description != nil {
		description := rewriteContent(*snapshot.SourceIssue.Description)
		snapshot.SourceIssue.Description = &description
	}
	for i := range snapshot.CommentThread {
		rewrite(snapshot.CommentThread[i].Attachments)
		snapshot.CommentThread[i].Content = rewriteContent(snapshot.CommentThread[i].Content)
	}
	snapshot.Version = SourceContextVersion
	snapshot.CapturedByUserID = util.UUIDToString(capturedBy)
	snapshot.CapturedAt = capturedAt.UTC().Format(time.RFC3339Nano)
	return SourceContextCapture{
		ID: contextID, WorkspaceID: workspaceID,
		SourceIssueID: issueUUID(build.Snapshot.SourceIssue.ID), AnchorCommentID: issueUUID(build.Snapshot.AnchorCommentID),
		CapturedByUserID: capturedBy, CapturedAt: capturedAt.UTC(), Snapshot: snapshot, Digest: build.Digest, Clones: clones,
	}, nil
}

func issueUUID(value string) pgtype.UUID {
	id, err := util.ParseUUID(value)
	if err != nil {
		panic(err)
	}
	return id
}

// PersistSourceContext 把一次捕获落库：上下文行 + 各克隆附件行，并清理该上下文待处理的删除意图。
// issueID 有效即视为已附着（attached），否则保持 pending（等后续创建附着）。
func PersistSourceContext(ctx context.Context, q *db.Queries, capture SourceContextCapture, issueID, taskID pgtype.UUID) (db.IssueSourceContext, error) {
	state := "pending"
	var attachedAt pgtype.Timestamptz
	if issueID.Valid {
		state = "attached"
		attachedAt = pgtype.Timestamptz{Time: capture.CapturedAt, Valid: true}
	}
	snapshotJSON, err := json.Marshal(capture.Snapshot)
	if err != nil {
		return db.IssueSourceContext{}, err
	}
	row, err := q.CreateIssueSourceContext(ctx, db.CreateIssueSourceContextParams{
		ID: capture.ID, WorkspaceID: capture.WorkspaceID, IssueID: issueID, OriginTaskID: taskID,
		SourceIssueID: capture.SourceIssueID, AnchorCommentID: capture.AnchorCommentID,
		CapturedByUserID: capture.CapturedByUserID, SnapshotVersion: SourceContextVersion,
		Snapshot: snapshotJSON, CaptureDigest: capture.Digest, State: state,
		CapturedAt: pgtype.Timestamptz{Time: capture.CapturedAt, Valid: true}, AttachedAt: attachedAt,
	})
	if err != nil {
		return db.IssueSourceContext{}, err
	}
	for _, clone := range capture.Clones {
		if _, err := q.CreateSourceContextAttachment(ctx, db.CreateSourceContextAttachmentParams{
			ID: clone.ID, WorkspaceID: capture.WorkspaceID, SourceContextID: capture.ID,
			UploaderType: "member", UploaderID: capture.CapturedByUserID,
			Filename: clone.Filename, Url: clone.URL, ContentType: clone.ContentType, SizeBytes: clone.SizeBytes,
		}); err != nil {
			return db.IssueSourceContext{}, err
		}
	}
	intents, err := q.DeletePendingSourceContextObjectIntents(ctx, db.DeletePendingSourceContextObjectIntentsParams{
		WorkspaceID: capture.WorkspaceID, SourceContextID: capture.ID,
	})
	if err != nil {
		return db.IssueSourceContext{}, fmt.Errorf("clear source context object intents: %w", err)
	}
	if len(intents) != len(capture.Clones) {
		return db.IssueSourceContext{}, fmt.Errorf("source context object intent ownership changed: cleared %d, want %d", len(intents), len(capture.Clones))
	}
	return row, nil
}

// CleanupSourceContextObjectIntents 对账「从未走到上下文事务」的克隆上传（请求取消、崩溃、存储/DB
// 失败边界模糊）。一小时结算窗口由 DB 状态认领围栏保护：一旦清理方占有了 intent，PersistSourceContext
// 就无法删掉完整的 pending intent 集合，只能回滚而不是把正被回收的对象挂上去。
func (s *TaskService) CleanupSourceContextObjectIntents(ctx context.Context, limit int) (int, error) {
	if s == nil || s.SourceContextStorage == nil || limit <= 0 {
		return 0, nil
	}
	cleaned := 0
	// limit 约束的是「尝试次数」而不是「成功数」。只数成功会让一轮的工作量与到期 intent 数成正比
	// 而非与批次大小成正比——这正是存储故障时的表现：每次删除都失败、成功数不动，轮次却一直认领
	// 下一行。
	for attempt := 0; attempt < limit; attempt++ {
		if ctx.Err() != nil {
			return cleaned, nil
		}
		leaseToken := dbid.NewV7()
		intent, err := s.Queries.ClaimSourceContextObjectIntentForCleanup(ctx, leaseToken)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			// 一轮在认领中途花光预算，是正常收尾而非值得告警的失败；下一轮从这里继续。
			if ctx.Err() != nil {
				return cleaned, nil
			}
			return cleaned, fmt.Errorf("claim source context object intent: %w", err)
		}
		referenced, err := s.Queries.SourceContextObjectIntentIsReferenced(ctx, db.SourceContextObjectIntentIsReferencedParams{
			WorkspaceID: intent.WorkspaceID, SourceContextID: intent.SourceContextID,
			AttachmentID: intent.AttachmentID, ObjectUrl: intent.ObjectUrl,
		})
		if err != nil {
			s.releaseSourceContextObjectIntent(ctx, intent, leaseToken, err)
			return cleaned, fmt.Errorf("check source context object reference: %w", err)
		}
		if !referenced {
			deleteCtx, cancel := context.WithTimeout(ctx, sourceContextObjectDeleteTimeout)
			deleteErr := s.SourceContextStorage.DeleteObject(deleteCtx, intent.StorageKey)
			cancel()
			if deleteErr != nil {
				s.releaseSourceContextObjectIntent(ctx, intent, leaseToken, deleteErr)
				continue
			}
		}
		changed, err := s.Queries.DeleteClaimedSourceContextObjectIntent(ctx, db.DeleteClaimedSourceContextObjectIntentParams{
			StorageKey: intent.StorageKey, WorkspaceID: intent.WorkspaceID, LeaseToken: leaseToken,
		})
		if err != nil || changed != 1 {
			return cleaned, fmt.Errorf("delete source context object intent: changed=%d: %w", changed, err)
		}
		cleaned++
	}
	return cleaned, nil
}

// releaseSourceContextObjectIntent 为本轮没解决的 intent 盖上重试退避。该写入刻意脱离调用方轮次
// 预算：当正是预算拦下删除时，一次被取消的 release 会让行在无退避状态下可被认领——下一轮又会挑中
// 同一个不健康对象，而不是推进其余工作。
func (s *TaskService) releaseSourceContextObjectIntent(ctx context.Context, intent db.IssueSourceContextObjectIntent, leaseToken pgtype.UUID, cause error) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceContextIntentReleaseTimeout)
	defer cancel()
	if _, err := s.Queries.ReleaseSourceContextObjectIntent(releaseCtx, db.ReleaseSourceContextObjectIntentParams{
		LastError: pgtype.Text{String: cause.Error(), Valid: true}, StorageKey: intent.StorageKey,
		WorkspaceID: intent.WorkspaceID, LeaseToken: leaseToken,
	}); err != nil {
		slog.Warn("source context cleanup: failed to record object intent retry backoff",
			"storage_key", intent.StorageKey, "error", err)
	}
}

// CleanupAbandonedSourceContexts 只在 30 天重试窗口之后放弃终态的 quick-create 捕获。重试创建会把
// origin_task_id 转给后继者，所以这里选中的终态行必是最新一次尝试。对象删除刻意放在状态事务提交后、
// 不持行锁执行；删除失败会留下被弃上下文与其附件行，供幂等重试。当对象全部清掉后，附件清单与完整
// 快照才一并移除。
func (s *TaskService) CleanupAbandonedSourceContexts(ctx context.Context, limit int32) (int, error) {
	if s == nil || s.TxStarter == nil || s.SourceContextStorage == nil || limit <= 0 {
		return 0, nil
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin source context cleanup: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	contexts, err := qtx.ListExpiredPendingSourceContextsForCleanup(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("list expired source contexts: %w", err)
	}
	for _, sourceContext := range contexts {
		changed, err := qtx.AbandonIssueSourceContext(ctx, db.AbandonIssueSourceContextParams{
			ID: sourceContext.ID, WorkspaceID: sourceContext.WorkspaceID,
		})
		if err != nil || changed != 1 {
			return 0, fmt.Errorf("abandon expired source context: changed=%d: %w", changed, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit source context cleanup: %w", err)
	}

	abandoned, err := s.Queries.ListAbandonedSourceContextsForCleanup(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("list abandoned source contexts: %w", err)
	}
	removed := 0
	for _, sourceContext := range abandoned {
		// 轮次预算耗尽就干净地停。下面的每次删除都幂等，下一轮从仍被弃的状态继续。
		if ctx.Err() != nil {
			return removed, nil
		}
		attachments, err := s.Queries.ListAttachmentsBySourceContext(ctx, db.ListAttachmentsBySourceContextParams{
			WorkspaceID: sourceContext.WorkspaceID, SourceContextID: sourceContext.ID,
		})
		if err != nil {
			return removed, fmt.Errorf("list abandoned source context attachments: %w", err)
		}
		deleteFailed := false
		for _, attachment := range attachments {
			if ctx.Err() != nil {
				return removed, nil
			}
			key := s.SourceContextStorage.KeyFromURL(attachment.Url)
			deleteCtx, cancel := context.WithTimeout(ctx, sourceContextObjectDeleteTimeout)
			err := s.SourceContextStorage.DeleteObject(deleteCtx, key)
			cancel()
			if err != nil {
				deleteFailed = true
				slog.Warn("source context cleanup: object delete failed; retained row for retry",
					"source_context_id", util.UUIDToString(sourceContext.ID), "attachment_id", util.UUIDToString(attachment.ID), "error", err)
			}
		}
		if deleteFailed {
			continue
		}
		if _, err := s.Queries.DeleteAttachmentsBySourceContext(ctx, db.DeleteAttachmentsBySourceContextParams{
			WorkspaceID: sourceContext.WorkspaceID, SourceContextID: sourceContext.ID,
		}); err != nil {
			return removed, fmt.Errorf("delete abandoned source context attachment rows: %w", err)
		}
		changed, err := s.Queries.DeleteAbandonedIssueSourceContext(ctx, db.DeleteAbandonedIssueSourceContextParams{
			WorkspaceID: sourceContext.WorkspaceID, ID: sourceContext.ID,
		})
		if err != nil {
			return removed, fmt.Errorf("delete abandoned source context: %w", err)
		}
		// 多个 server 实例可能同时清扫同一条被弃行。存储删除是幂等的；删除 0 行意味着另一个实例
		// 赢下了最终 DB 清理——那正是我们想要的结果。
		if changed == 0 {
			continue
		}
		removed++
	}
	return removed, nil
}
