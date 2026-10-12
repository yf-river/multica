// issue.go —— issue 创建的服务层唯一入口（issue service 的本体）。
// 职责：把已解析好的 IssueCreateParams 原子地落成一条 issue——含父 issue/project 同工作区校验、
// 防重闸门、issue 编号分配、位置计算、可选 source-context 持久化、labels/attachments 绑定、
// 「指派即入队」逻辑与广播事件。刻意不依赖 http.Request：调用方自管传输层，传一个完整解析的
// IssueCreateParams 进来，这样 HTTP `POST /issues`、Lark `/issue` 等入口共享同一套边界语义。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// IssueService 是 issue 创建的服务层唯一入口。HTTP `POST /issues` 与未来的 Lark `/issue` 命令都调
// Create，让防重闸门、issue 编号、附件绑定、广播、analytics、agent/squad 入队保持对齐。本服务刻意
// 不依赖 http.Request——调用方自管解析，传一个完全解析好的 IssueCreateParams。
type IssueService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
	Analytics analytics.Client
	// Metrics 是共享的业务指标收集器。由 cmd/server/router.go 在构造后注入；测试/未挂 metrics
	// 监听的 self-hosted 环境下为 nil——obsmetrics.RecordEvent 把 nil Metrics 视为「仅 PostHog」，
	// 所以不设置是安全的。
	Metrics     *obsmetrics.BusinessMetrics
	TaskService *TaskService
	// restricted action 提供 Cloud 的生效 issue 数量指令。nil 即 self-hosted 的无限路径。
}

// NewIssueService 组装 IssueService。
func NewIssueService(q *db.Queries, tx TxStarter, bus *events.Bus, ac analytics.Client, ts *TaskService) *IssueService {
	return &IssueService{
		Queries:     q,
		TxStarter:   tx,
		Bus:         bus,
		Analytics:   ac,
		TaskService: ts,
	}
}

// IssueCreateParams 把已经过校验、已解析的输入带给 IssueService.Create。把「请求 payload → 本结构」
// 的解析步骤留给 handler 做，服务保持传输层无关。
type IssueCreateParams struct {
	WorkspaceID   pgtype.UUID
	Title         string
	Description   pgtype.Text
	Status        string
	Priority      string
	AssigneeType  pgtype.Text
	AssigneeID    pgtype.UUID
	CreatorType   string // "agent" 或 "member"
	CreatorID     pgtype.UUID
	ParentIssueID pgtype.UUID
	ProjectID     pgtype.UUID
	StartDate     pgtype.Date
	DueDate       pgtype.Date
	OriginType    pgtype.Text
	OriginID      pgtype.UUID
	AttachmentIDs []pgtype.UUID
	// LabelIDs 是要贴到新 issue 上的标签。它们在创建事务内被校验并写入（见 Create），所以 issue
	// 永远不会带着残缺或错误的标签集提交。未知或非 issue 级标签 id 会让整个创建以
	// ErrIssueLabelNotFound 失败，而不是被静默忽略。
	LabelIDs       []pgtype.UUID
	AllowDuplicate bool
	// Stage 把本 issue 归入父级下某个有序关卡组（NULL = 未分阶段）。见 issue_child_done.go 的
	// 阶段关卡唤醒。
	Stage pgtype.Int4
	// SourceContext 只由评论作用域的手动创建端点设置。它的不可变快照与克隆出的附件行随新 issue
	// 在同一事务里提交。
	SourceContext *SourceContextCapture
}

// IssueCreateOpts 聚合 IssueService.Create 的可选旋钮。多数调用方留零值即可。
type IssueCreateOpts struct {
	// RelatedRecord 在同一事务内登记业务来源，确保任务出现前其受控数据已可定位。
	RelatedRecord func(context.Context, pgx.Tx, db.Issue) error
	// BroadcastPayload 非 nil 时，在 issue 行创建、附件绑定后被调用，其返回值作为 EventIssueCreated
	// 的 payload 经事件总线发出。HTTP handler 用这个钩子注入自己的 IssueResponse，本包就不用依赖
	// handler 层类型。为 nil 时服务发一个极简 `{"issue_id": <uuid>}` payload——足够缓存失效，但期望
	// 完整响应形状的前端必须提供 BroadcastPayload。labels 参数是创建事务里绑定的权威快照，发出的
	// payload 因此能带上新 issue 的标签，所有在线客户端一开始渲染的就是已贴标签的 issue，而不是
	// 空白、等一次重取。
	BroadcastPayload func(issue db.Issue, attachments []db.Attachment, labels []db.IssueLabel) map[string]any

	// ActorID 覆盖广播 + analytics 用的 actor id——当它不同于行上的 creator 时。agent 创建的 issue
	// 在这里传 agent UUID（creator_id 列是 daemon owner）。为空则回退 CreatorID。
	ActorID string

	// AnalyticsAgentID 是与该 issue 关联、供 analytics 使用的 agent（assignee agent；agent 创建的
	// issue 则是 creator agent）。由调用方解析，因为它依赖传输层上下文。
	AnalyticsAgentID string

	// Platform 把 IssueCreated analytics + 业务指标事件标上请求来自的客户端面（web / daemon / lark /
	// autopilot）。来自 handler 层取中间件里的 client 元数据。
	Platform string

	// AssignedAgentRunFireAt 让自动创建的「指派 agent 任务」落在持久的延期状态。Channel /issue 在
	// 游离媒体仍在解析时用它，附件绑定完成后再把返回的任务提升为可运行。零值保留普通的立即入队路径。
	AssignedAgentRunFireAt time.Time
}

// ErrActiveDuplicate 表示防重闸门发现存在一条 (workspace, project, parent, title) 相同的活跃 issue，
// 而 AllowDuplicate 为 false。返回该错误时同时填充 IssueCreateResult.DuplicateIssue，好让调用方能
// 渲染冲突（HTTP 409、Lark 卡片等）。
var ErrActiveDuplicate = errors.New("active duplicate issue exists")

// ErrParentIssueNotFound 表示传入的 ParentIssueID 在该 issue 的工作区里不存在。服务拒绝创建孤儿或
// 跨工作区的子 issue；调用方把它翻译成各自传输层的 400 / Lark 卡片错误。
var ErrParentIssueNotFound = errors.New("parent issue not found in this workspace")

// ErrProjectNotFound 表示传入的 ProjectID 在该 issue 的工作区里不存在。跨工作区 project id 在这里被
// 拒绝，让每个创建入口（HTTP `POST /issues`、Lark `/issue`、未来的 MCP/API key 调用方）都执行同一
// 条工作区边界，不用各自记着。调用方把它翻译成 400。
var ErrProjectNotFound = errors.New("project not found in this workspace")

// ErrIssueLabelNotFound 表示某个 LabelID 不在该工作区、或不是 issue 级标签。整个创建被拒绝——新 issue
// 绝不能带着残缺或错误的标签集出生。调用方翻译成各自传输层的 400。
var ErrIssueLabelNotFound = errors.New("issue label not found in this workspace")

// ErrIssueStatusUnavailable 表示请求的自定义状态在「调用方预检」与「创建事务」之间被归档。调用方
// 翻译成 409——请求到达时是合法的，对着刷新后的目录重试即是对症解药。
var ErrIssueStatusUnavailable = errors.New("issue status is no longer available")

// ErrSourceContextAlreadyAttached 表示该 quick-create 来源任务已有挂上的 source context，不能重复挂。
var ErrSourceContextAlreadyAttached = errors.New("source context is already attached")

// IssueCreateResult 是 IssueService.Create 的类型化返回值。
//
//   - happy path：Issue 是新行，Attachments 列出已绑定的附件（可为空），DuplicateIssue 为 nil。
//   - 遇 ErrActiveDuplicate：DuplicateIssue 是挡住创建的那条行；Issue 与 Attachments 为零值。
type IssueCreateResult struct {
	Issue       db.Issue
	Attachments []db.Attachment
	// AssignedTaskID 在 Create 为 agent assignee 入队自动任务时填充，包括被 AssignedAgentRunFireAt
	// 延期的任务。
	AssignedTaskID pgtype.UUID
	// Labels 是创建事务中贴到新 issue 上的权威标签集（未请求时为空）。调用方把它回显在创建响应 +
	// issue:created 事件上，让每个客户端渲染出的新 issue 已是贴好标签的样子，新客户端也能借此发现
	// 后端确实理解 label_ids（见 create handler 的兼容契约）。
	Labels         []db.IssueLabel
	DuplicateIssue *db.Issue
}

// Create 端到端地、原子地跑完整个 issue 创建流水线：
//
//  1. 开事务。
//  2. 解析并校验 parent / project 属于同一工作区。
//  3. 加锁并检查防重闸门。
//  4. 递增工作区 issue 计数器。
//  5. 插入 issue 行（可带来源戳）。
//  6. 提交。
//  7. 绑定任何已预上传的附件（提交后、幂等）。
//  8. 媒体闸门的 channel issue 会在 issue 事务里持久化其延期的「指派 agent 任务」，让两行原子可见；
//     普通创建保持既有的「先事件后入队」顺序。
//  9. 向总线发布 EventIssueCreated（payload 经 opts.BroadcastPayload）。
//  10. 上报 IssueCreated analytics 事件。
//  11. issue 已被指派且不在 `backlog` 时，入队普通 agent 任务或触发 squad leader。
//
// 归在服务层的校验（parent 存在性、project 工作区归属、parent → project 反填）在这里强制执行，
// 让每个创建入口——HTTP `POST /issues`、Lark `/issue`、未来 MCP/API-key 调用方——共享同一套工作区
// 边界语义。调用方自留的校验只限传输层形状检查：标题必填、RFC3339 日期格式、assignee 组合合理性。
func (s *IssueService) Create(ctx context.Context, p IssueCreateParams, opts IssueCreateOpts) (IssueCreateResult, error) {
	actorID := opts.ActorID
	if actorID == "" {
		actorID = util.UUIDToString(p.CreatorID)
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	if p.SourceContext != nil {
		if _, err := qtx.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{
			ID: p.SourceContext.SourceIssueID, WorkspaceID: p.WorkspaceID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return IssueCreateResult{}, ErrSourceIssueDeleted
			}
			return IssueCreateResult{}, fmt.Errorf("lock source issue: %w", err)
		}
		locked, err := qtx.LockCommentAncestorPath(ctx, db.LockCommentAncestorPathParams{
			CommentID: p.SourceContext.AnchorCommentID, WorkspaceID: p.WorkspaceID,
			IssueID: p.SourceContext.SourceIssueID,
		})
		if err != nil {
			return IssueCreateResult{}, fmt.Errorf("lock anchor comment thread: %w", err)
		}
		if len(locked) == 0 {
			return IssueCreateResult{}, ErrAnchorCommentDeleted
		}
		current, err := BuildSourceContext(ctx, qtx, p.WorkspaceID, p.SourceContext.AnchorCommentID)
		if err != nil {
			return IssueCreateResult{}, err
		}
		if current.Digest != p.SourceContext.Digest {
			return IssueCreateResult{}, ErrSourceContextChanged
		}
	}

	// 落在 CUSTOM 状态的创建要拿共享的目录锁、并在这个事务里重新解析一次状态。调用方在事务开启前
	// 校验过状态——那时足够早到能干净地返回 400，但对安全而言还是太早：之间可能提交一次归档。
	// 在锁内复查，才是让状态在行被写入的那一刻被证实为 active。内置状态两个都跳过——它们永远不会被
	// 归档，常见路径因此保持不变。（MUL-6243）
	if !issuestatus.IsBuiltIn(p.Status) {
		if err := qtx.LockIssueStatusCatalogShared(ctx, p.WorkspaceID); err != nil {
			return IssueCreateResult{}, err
		}
		if _, err := issuestatus.Resolve(ctx, qtx, p.WorkspaceID, p.Status); err != nil {
			if errors.Is(err, issuestatus.ErrUnknownStatus) {
				return IssueCreateResult{}, ErrIssueStatusUnavailable
			}
			return IssueCreateResult{}, err
		}
	}

	// 在碰防重闸门之前先解析并校验 parent / project，伪造的 parent 或 project id 因此在我们动 issue
	// 计数器之前就被拒绝。两项检查都以 WorkspaceID 为界——从这个服务出发，没有任何路径能到达
	// 外部工作区的行。
	projectID := p.ProjectID
	if p.ParentIssueID.Valid {
		parent, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          p.ParentIssueID,
			WorkspaceID: p.WorkspaceID,
		})
		if err != nil || !parent.ID.Valid {
			return IssueCreateResult{}, ErrParentIssueNotFound
		}
		// 调用方没有显式钉住 project 时，从 parent 反填。与长期存在的 HTTP 行为一致：子 issue 在未
		// 覆盖时继承其 parent 的 project。
		if !projectID.Valid {
			projectID = parent.ProjectID
		}
	}
	if projectID.Valid {
		if _, err := qtx.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID:          projectID,
			WorkspaceID: p.WorkspaceID,
		}); err != nil {
			return IssueCreateResult{}, ErrProjectNotFound
		}
	}

	// 在递增 issue 计数器之前校验标签，让过期或错作用域的勾选廉价地失败。去重后的行会在下面这个
	// 同一事务里贴到 issue 上，并在结果里作为权威快照回显。
	labels, err := validateIssueLabels(ctx, qtx, p.WorkspaceID, p.LabelIDs)
	if err != nil {
		return IssueCreateResult{}, err
	}

	duplicate, found, err := issueguard.LockAndFindActiveDuplicate(ctx, qtx, p.WorkspaceID, projectID, p.ParentIssueID, p.Title, p.AllowDuplicate)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("duplicate guard: %w", err)
	}
	if found {
		dup := duplicate
		return IssueCreateResult{DuplicateIssue: &dup}, ErrActiveDuplicate
	}

	issueNumber, err := qtx.IncrementIssueCounter(ctx, p.WorkspaceID)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("allocate issue number: %w", err)
	}

	// 新 issue 排到其 (workspace, status) 列顶部，供手动排序。在事务内、IncrementIssueCounter 已拿
	// 工作区行锁之后计算——同工作区两个并发创建因此能看见彼此的 position，不会同时落在同一个
	// min-1 槽位上。经 UpdateIssue(position) 的并发手动重排不拿这个锁，所以创建与重排赛跑时仍可能
	// 撞 position——手动排序本就是尽力而为，UI 靠次要 ORDER BY 键容忍相同 position。
	newPosition, err := issueposition.NextTopPosition(ctx, tx, p.WorkspaceID, p.Status)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("next top position: %w", err)
	}

	var issue db.Issue
	var assignedTask db.AgentTaskQueue
	var attachments []db.Attachment
	var createdEvent events.Event
	if p.OriginType.Valid {
		issue, err = qtx.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   p.WorkspaceID,
			Title:         p.Title,
			Description:   p.Description,
			Status:        p.Status,
			Priority:      p.Priority,
			AssigneeType:  p.AssigneeType,
			AssigneeID:    p.AssigneeID,
			CreatorType:   p.CreatorType,
			CreatorID:     p.CreatorID,
			ParentIssueID: p.ParentIssueID,
			Position:      newPosition,
			StartDate:     p.StartDate,
			DueDate:       p.DueDate,
			Number:        issueNumber,
			ProjectID:     projectID,
			OriginType:    p.OriginType,
			OriginID:      p.OriginID,
			Stage:         p.Stage,
		})
	} else {
		issue, err = qtx.CreateIssue(ctx, db.CreateIssueParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   p.WorkspaceID,
			Title:         p.Title,
			Description:   p.Description,
			Status:        p.Status,
			Priority:      p.Priority,
			AssigneeType:  p.AssigneeType,
			AssigneeID:    p.AssigneeID,
			CreatorType:   p.CreatorType,
			CreatorID:     p.CreatorID,
			ParentIssueID: p.ParentIssueID,
			Position:      newPosition,
			StartDate:     p.StartDate,
			DueDate:       p.DueDate,
			Number:        issueNumber,
			ProjectID:     projectID,
			Stage:         p.Stage,
		})
	}
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("create issue: %w", err)
	}

	if p.SourceContext != nil {
		if _, err := PersistSourceContext(ctx, qtx, *p.SourceContext, issue.ID, pgtype.UUID{}); err != nil {
			return IssueCreateResult{}, fmt.Errorf("persist source context: %w", err)
		}
	} else if p.OriginType.Valid && p.OriginType.String == "quick_create" && p.OriginID.Valid {
		task, taskErr := qtx.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
			ID: p.OriginID, WorkspaceID: p.WorkspaceID,
		})
		if taskErr != nil {
			return IssueCreateResult{}, fmt.Errorf("load quick-create origin task: %w", taskErr)
		}
		if p.CreatorType != "agent" || !p.CreatorID.Valid || p.CreatorID != task.AgentID {
			return IssueCreateResult{}, errors.New("quick-create origin task does not belong to the creating agent")
		}
		var quickCreate QuickCreateContext
		if err := json.Unmarshal(task.Context, &quickCreate); err != nil {
			return IssueCreateResult{}, fmt.Errorf("decode quick-create origin context: %w", err)
		}
		if quickCreate.Type != QuickCreateContextType {
			return IssueCreateResult{}, errors.New("quick-create origin task has invalid context type")
		}
		contextWorkspaceID, parseErr := util.ParseUUID(quickCreate.WorkspaceID)
		if parseErr != nil || contextWorkspaceID != p.WorkspaceID {
			return IssueCreateResult{}, errors.New("quick-create origin context has invalid workspace")
		}
		if quickCreate.SourceContextID != "" {
			contextID, parseErr := util.ParseUUID(quickCreate.SourceContextID)
			if parseErr != nil {
				return IssueCreateResult{}, fmt.Errorf("invalid quick-create source context id: %w", parseErr)
			}
			requesterID, parseErr := util.ParseUUID(quickCreate.RequesterID)
			if parseErr != nil || !task.OriginatorUserID.Valid || requesterID != task.OriginatorUserID {
				return IssueCreateResult{}, errors.New("quick-create source context has invalid requester")
			}
			pending, pendingErr := qtx.GetPendingIssueSourceContextByOriginTask(ctx, db.GetPendingIssueSourceContextByOriginTaskParams{
				WorkspaceID: p.WorkspaceID, OriginTaskID: task.ID,
			})
			if pendingErr != nil {
				if errors.Is(pendingErr, pgx.ErrNoRows) {
					return IssueCreateResult{}, ErrSourceContextAlreadyAttached
				}
				return IssueCreateResult{}, fmt.Errorf("load pending quick-create source context: %w", pendingErr)
			}
			if pending.ID != contextID || pending.CapturedByUserID != requesterID {
				return IssueCreateResult{}, errors.New("quick-create source context ownership mismatch")
			}
			if _, attachErr := qtx.AttachIssueSourceContext(ctx, db.AttachIssueSourceContextParams{
				IssueID: issue.ID, WorkspaceID: p.WorkspaceID, ID: contextID, OriginTaskID: task.ID,
			}); attachErr != nil {
				if errors.Is(attachErr, pgx.ErrNoRows) {
					return IssueCreateResult{}, ErrSourceContextAlreadyAttached
				}
				return IssueCreateResult{}, fmt.Errorf("attach quick-create source context: %w", attachErr)
			}
		}
	}

	// 在创建事务里贴标签，issue 与其标签一起提交——老流程先建 issue、再在第二个非原子往返里贴标签，
	// 其部分失败会让 issue 归错类。AttachLabelToIssue 是 workspace/resource_type 双重守卫 + ON
	// CONFLICT DO NOTHING，且 id 上面已校验过。
	for _, label := range labels {
		if err := qtx.AttachLabelToIssueOnCreate(ctx, db.AttachLabelToIssueOnCreateParams{
			IssueID:     issue.ID,
			LabelID:     label.ID,
			WorkspaceID: p.WorkspaceID,
		}); err != nil {
			return IssueCreateResult{}, fmt.Errorf("attach issue label: %w", err)
		}
	}

	// 在 issue 事件记录前绑定预上传的附件。事件快照与 issue 行因此同时可见——重放的客户端绝不会
	// 看到一个文件悄悄丢失的 issue。
	if len(p.AttachmentIDs) > 0 {
		if _, err := qtx.LockAttachmentsForIssueLink(ctx, db.LockAttachmentsForIssueLinkParams{
			WorkspaceID: p.WorkspaceID, AttachmentIds: p.AttachmentIDs,
		}); err != nil {
			return IssueCreateResult{}, fmt.Errorf("lock issue attachments: %w", err)
		}
		if _, err := qtx.LinkAttachmentsToIssue(ctx, db.LinkAttachmentsToIssueParams{
			IssueID: issue.ID, WorkspaceID: p.WorkspaceID, AttachmentIds: p.AttachmentIDs,
			BumpRevision: false,
		}); err != nil {
			return IssueCreateResult{}, fmt.Errorf("link issue attachments: %w", err)
		}
		attachments, err = qtx.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{
			IssueID: issue.ID, WorkspaceID: p.WorkspaceID,
		})
		if err != nil {
			return IssueCreateResult{}, fmt.Errorf("list issue attachments: %w", err)
		}
	}

	if !opts.AssignedAgentRunFireAt.IsZero() && s.shouldEnqueueAgentTaskWithQueries(ctx, qtx, issue) {
		// issue 绝不能先于其媒体闸门的指派任务可见。经 qtx 插入两行让唯一索引赢家确定：任何能发现
		// 已提交 issue 的观察者，也必然看到这条惰性延期任务，并必须并入它。
		assignedTask, err = s.TaskService.createDeferredChannelIssueTaskWithQueries(ctx, qtx, issue, opts.AssignedAgentRunFireAt)
		if err != nil {
			return IssueCreateResult{}, fmt.Errorf("create deferred channel issue task: %w", err)
		}
	}

	createdEvent = issueCreatedEvent(issue, attachments, labels, p.CreatorType, actorID, opts)
	createdEvent, err = recordDurableEventTx(ctx, qtx, createdEvent)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("record issue-created event: %w", err)
	}

	if opts.RelatedRecord != nil {
		if err := opts.RelatedRecord(ctx, tx, issue); err != nil {
			return IssueCreateResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return IssueCreateResult{}, fmt.Errorf("commit: %w", err)
	}

	var assignedTaskID pgtype.UUID
	if !opts.AssignedAgentRunFireAt.IsZero() {
		assignedTaskID = assignedTask.ID
		if assignedTaskID.Valid {
			if err := s.TaskService.hydrateDeferredChannelIssueTaskOverlay(ctx, assignedTask); err != nil {
				// runtime overlay 在所有入队路径上都是尽力而为。任务已持久且安全地延期，一次可选的
				// 集成失败绝不能把已提交的 issue 变成重试重复。
				slog.Warn("hydrate deferred channel issue task overlay failed",
					"issue_id", util.UUIDToString(issue.ID),
					"task_id", util.UUIDToString(assignedTask.ID),
					"error", err)
			}
		} else if s.shouldEnqueueSquadLeaderOnAssign(ctx, issue) {
			// AssignedAgentRunFireAt 目前归 channel /issue 所有，而它总是解析出 agent assignee。
			// 为任何将来以 squad 提供该选项的调用方保留普通 squad 路径。
			s.enqueueSquadLeaderTask(ctx, issue, pgtype.UUID{}, p.CreatorType, actorID)
		}
	}

	publishCommittedEvent(s.Bus, createdEvent)
	s.captureCreatedAnalytics(issue, p.CreatorType, actorID, opts)
	if opts.AssignedAgentRunFireAt.IsZero() {
		assignedTaskID = s.maybeEnqueueOnAssign(ctx, issue, p.CreatorType, actorID, opts.AssignedAgentRunFireAt)
	}

	return IssueCreateResult{Issue: issue, Attachments: attachments, Labels: labels, AssignedTaskID: assignedTaskID}, nil
}

// validateIssueLabels 检查每个请求的标签都存在于工作区且是 issue 级，返回去重后的标签行供绑定。
// 返回整行（而非仅 id）让 Create 能在创建响应 + issue:created 事件上回显权威标签快照，不用二次查询。
// 它镜像 AttachLabelToIssue 已强制的 workspace + resource_type='issue' 守卫，让未知或错作用域的 id
// 以 ErrIssueLabelNotFound 浮现，而不是静默的空插入。无效（零值）UUID 跳过。单条 issue 的标签数
// 很小，所以按去重 id 逐个 GetLabel 即可，也避免引入新的批量查询。
func validateIssueLabels(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, labelIDs []pgtype.UUID) ([]db.IssueLabel, error) {
	if len(labelIDs) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(labelIDs))
	deduped := make([]db.IssueLabel, 0, len(labelIDs))
	for _, labelID := range labelIDs {
		if !labelID.Valid {
			continue
		}
		key := util.UUIDToString(labelID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		label, err := qtx.GetLabel(ctx, db.GetLabelParams{
			ID:          labelID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrIssueLabelNotFound
			}
			return nil, fmt.Errorf("get issue label: %w", err)
		}
		if label.ResourceType != "issue" {
			return nil, ErrIssueLabelNotFound
		}
		deduped = append(deduped, label)
	}
	return deduped, nil
}

// linkAttachments 把给定附件 id 绑到新 issue 上，返回重取的附件行，让调用方拼响应时不用二次查询。
// 出错只记日志并吞掉——附件绑定是提交后尽力而为的一步，一条过期附件行不值得让整个创建失败。
func (s *IssueService) linkAttachments(ctx context.Context, issue db.Issue, ids []pgtype.UUID) []db.Attachment {
	if len(ids) == 0 {
		return nil
	}
	if _, err := s.Queries.LinkAttachmentsToIssue(ctx, db.LinkAttachmentsToIssueParams{
		IssueID:       issue.ID,
		WorkspaceID:   issue.WorkspaceID,
		AttachmentIds: ids,
		BumpRevision:  false,
	}); err != nil {
		slog.Error("failed to link attachments to issue",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err)
		return nil
	}
	list, err := s.Queries.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("failed to list attachments for new issue",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err)
		return nil
	}
	return list
}

// issueCreatedEvent 构造每个 issue 创建入口共用的持久化信封。issue id 是逻辑上的 occurrence 身份：
// 重试可以重跑 handler，但绝不能造出第二条 create 事件。
func issueCreatedEvent(issue db.Issue, attachments []db.Attachment, labels []db.IssueLabel, creatorType, actorID string, opts IssueCreateOpts) events.Event {
	var payload map[string]any
	if opts.BroadcastPayload != nil {
		payload = opts.BroadcastPayload(issue, attachments, labels)
	} else {
		payload = map[string]any{"issue_id": util.UUIDToString(issue.ID)}
	}
	return events.Event{
		Type:           protocol.EventIssueCreated,
		IdempotencyKey: "issue:created:" + util.UUIDToString(issue.ID),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      creatorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// publishIssueCreated 直接向总线实时发布一条 issue:created 事件（非持久化版本）。
func (s *IssueService) publishIssueCreated(issue db.Issue, attachments []db.Attachment, labels []db.IssueLabel, creatorType, actorID string, opts IssueCreateOpts) {
	if s.Bus == nil {
		return
	}
	var payload map[string]any
	if opts.BroadcastPayload != nil {
		payload = opts.BroadcastPayload(issue, attachments, labels)
	} else {
		// 极简兜底：即便调用方忘了传 builder，缓存失效仍会触发。期望完整 IssueResponse 的前端
		// 必须传 BroadcastPayload。
		payload = map[string]any{"issue_id": util.UUIDToString(issue.ID)}
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueCreated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   creatorType,
		ActorID:     actorID,
		Payload:     payload,
	})
}

// PublishAttachmentsChanged 在一次游离的 channel 媒体事务后刷新附件与 issue 投影。issue 创建是
// 在远端下载完成前就广播的，所以附件事件给当前客户端补上缓存缺口。issue:updated 事件经由所有
// 当前客户端都懂的协议，携带新物化的描述。
func (s *IssueService) PublishAttachmentsChanged(ctx context.Context, issue db.Issue, actorID pgtype.UUID) {
	if s.Bus == nil {
		return
	}
	if s.Queries == nil || s.TxStarter == nil {
		s.publishIssueAttachmentsChanged(issue, actorID, 0)
		return
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("begin attachment projection transaction failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	current, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("failed to load issue after channel media bind",
			"issue_id", util.UUIDToString(issue.ID), "error", err)
		return
	}
	workspace, err := qtx.GetWorkspace(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("failed to load workspace after channel media bind",
			"workspace_id", util.UUIDToString(issue.WorkspaceID), "error", err)
		return
	}
	updatedEvent := events.Event{
		Type:           protocol.EventIssueUpdated,
		IdempotencyKey: "issue:channel-media:" + util.UUIDToString(current.ID) + ":" + strconv.FormatInt(current.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(current.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      "member",
		ActorID:        util.UUIDToString(actorID),
		Payload: map[string]any{
			"issue":            IssueToMapResolved(ctx, s.Queries, current, workspace.IssuePrefix),
			"assignee_changed": false,
			"status_changed":   false,
			"project_changed":  false,
		},
	}
	attachmentsEvent := events.Event{
		Type:           protocol.EventIssueAttachmentsChanged,
		IdempotencyKey: "issue:attachments:" + util.UUIDToString(current.ID) + ":" + strconv.FormatInt(current.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(current.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      "member",
		ActorID:        util.UUIDToString(actorID),
		Payload: map[string]any{
			"issue_id":       util.UUIDToString(current.ID),
			"issue_revision": current.Revision,
		},
	}
	updatedEvent, err = eventoutbox.Enqueue(ctx, qtx, updatedEvent)
	if err != nil {
		return
	}
	attachmentsEvent, err = eventoutbox.Enqueue(ctx, qtx, attachmentsEvent)
	if err != nil {
		return
	}
	if err := tx.Commit(ctx); err != nil {
		return
	}
	// 只有在该 revision 的完整 owner 快照之后才发布这条辅助投影。把这两条事件反过来，会让
	// revision 敏感的客户端把 issue:updated payload 当成同 revision 重复而拒绝。
	if s.Bus != nil {
		s.Bus.PublishRecovered(updatedEvent)
		s.Bus.PublishRecovered(attachmentsEvent)
	}
}

// publishIssueAttachmentsChanged 是 PublishAttachmentsChanged 的无事务降级路径：只发附件事件，
// 不刷新 issue 投影。
func (s *IssueService) publishIssueAttachmentsChanged(issue db.Issue, actorID pgtype.UUID, revision int64) {
	payload := map[string]any{"issue_id": util.UUIDToString(issue.ID)}
	if revision > 0 {
		payload["issue_revision"] = revision
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueAttachmentsChanged,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "member",
		ActorID:     util.UUIDToString(actorID),
		Payload:     payload,
	})
}

// captureCreatedAnalytics 上报 IssueCreated 分析事件。agent 创建的 issue，actor 字符串加 "agent:"
// 前缀以区分于 member。
func (s *IssueService) captureCreatedAnalytics(issue db.Issue, creatorType, actorID string, opts IssueCreateOpts) {
	if s.Analytics == nil {
		return
	}
	source, taskID, autopilotRunID := classifyOrigin(issue, opts)
	analyticsActorID := actorID
	if creatorType == "agent" {
		analyticsActorID = "agent:" + actorID
	}
	obsmetrics.RecordEvent(s.Analytics, s.Metrics, analytics.IssueCreated(
		analyticsActorID,
		util.UUIDToString(issue.WorkspaceID),
		util.UUIDToString(issue.ID),
		opts.AnalyticsAgentID,
		taskID,
		autopilotRunID,
		source,
		opts.Platform,
	))
}

// classifyOrigin 把 issue 的 origin_type / origin_id 列映射成 analytics 来源标签。未知 origin_type
// 回退到 SourceManual 并记一条警告——analytics 口径漂移好过把事件整个丢掉。
func classifyOrigin(issue db.Issue, opts IssueCreateOpts) (source, taskID, autopilotRunID string) {
	source = analytics.SourceManual
	if !issue.OriginType.Valid {
		return source, "", ""
	}
	originID := util.UUIDToString(issue.OriginID)
	switch issue.OriginType.String {
	case "quick_create", "agent_create":
		// 两者都把 issue 回连到创建它的 agent_task_queue 行（agent_create 是普通 agent `issue create`
		// 路径，MUL-4305）；把这个 task id 露出来，来源标签仍保留 manual。
		return analytics.SourceManual, originID, ""
	case "autopilot":
		return analytics.SourceAutopilot, "", originID
	default:
		slog.Warn("analytics: unknown issue origin type",
			"origin_type", issue.OriginType.String,
			"issue_id", util.UUIDToString(issue.ID),
		)
		return analytics.SourceManual, "", ""
	}
}

// maybeEnqueueOnAssign 在 issue 创建提交后处理「指派即入队」：agent 就绪就入队（必要时延期），
// squad 就绪就入队 squad leader 任务。返回实际入队成功的任务 id（无任务则零值）。
func (s *IssueService) maybeEnqueueOnAssign(ctx context.Context, issue db.Issue, creatorType, actorID string, agentRunFireAt time.Time) pgtype.UUID {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return pgtype.UUID{}
	}
	// Backlog 是停车场：从那里什么都不跑，所以这里也无需多言。落在 backlog 分类的自定义状态同样
	// 停着。（MUL-6243）
	if issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status) == "backlog" {
		return pgtype.UUID{}
	}
	verdict, admitted := agentAssigneeVerdict(ctx, s.runtimeLookup(s.Queries), issue)
	if !admitted && verdict.Reason == dispatch.ReasonRuntimeUnusable {
		// 指派没有一条「指派人会读」的响应来表达这个结果，所以拒绝在 issue 上自解释，而不是凭空
		// 消失（MUL-6164）。只在这里做、不在上面的 create-with-assignee 路径里做：那条路径跑在 issue
		// 事务内，一条关于某台机器的说明，无权决定 issue 本身是否提交。
		s.noteRuntimeUnusable(ctx, issue, verdict)
	}
	if admitted {
		var task db.AgentTaskQueue
		var err error
		if agentRunFireAt.IsZero() {
			task, err = s.TaskService.EnqueueTaskForIssue(ctx, issue)
		} else {
			task, err = s.TaskService.EnqueueDeferredChannelIssueTask(ctx, issue, agentRunFireAt)
		}
		if err != nil {
			slog.Warn("enqueue agent task on create failed",
				"issue_id", util.UUIDToString(issue.ID),
				"error", err)
		} else {
			return task.ID
		}
	}
	if s.shouldEnqueueSquadLeaderOnAssign(ctx, issue) {
		s.enqueueSquadLeaderTask(ctx, issue, pgtype.UUID{}, creatorType, actorID)
	}
	return pgtype.UUID{}
}

// shouldEnqueueAgentTaskWithQueries 返回「issue 创建是否应触发被指派的 agent」。backlog issue 跳过——
// backlog 是用来预指派而不立即执行的停车场。指派路径经 agentAssigneeVerdict 做同样测试，它还能
// 告诉调用方拒绝的 WHY；本函数跑在创建事务内，那里还无可告知的对象。
//
// 镜像 handler.shouldEnqueueAgentTask；留在这里让服务自足——两条代码路径必须一起移动。
func (s *IssueService) shouldEnqueueAgentTaskWithQueries(ctx context.Context, q *db.Queries, issue db.Issue) bool {
	// 经 q 而非 s.Queries 解析：本函数跑在创建事务内，必须与事务其余部分看到同一快照。（MUL-6243）
	if issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) == "backlog" {
		return false
	}
	return isAgentAssigneeReadyWithQueries(ctx, s.runtimeLookup(q), issue)
}

// isAgentAssigneeReadyWithQueries 是 agentAssigneeVerdict 的薄封装：只回「是否放行」。
func isAgentAssigneeReadyWithQueries(ctx context.Context, lookup RuntimeLookup, issue db.Issue) bool {
	_, ok := agentAssigneeVerdict(ctx, lookup, issue)
	return ok
}

// agentAssigneeVerdict 把 issue 的 agent assignee 送进共享的就绪检查，报告能否为它入队工作，以及
// 不能时的 verdict。
//
// 只有 BLOCKED verdict 会拦下入队。仅离线仍要排队：那部分工作在笔记本回来时运行，大家就靠它。
func agentAssigneeVerdict(ctx context.Context, lookup RuntimeLookup, issue db.Issue) (AgentVerdict, bool) {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return AgentVerdict{}, false
	}
	agent, err := lookup.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil {
		return AgentVerdict{}, false
	}
	verdict, err := AgentReadiness(ctx, lookup, agent)
	if err != nil {
		return AgentVerdict{}, false
	}
	return verdict, !verdict.Blocked()
}

// shouldEnqueueSquadLeaderOnAssign 判断「指派给 squad 的 issue 创建」是否要入队 leader 任务：
// backlog 状态不发；否则看 leader 的就绪结论。
func (s *IssueService) shouldEnqueueSquadLeaderOnAssign(ctx context.Context, issue db.Issue) bool {
	if issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status) == "backlog" {
		return false
	}
	return s.isSquadLeaderReady(ctx, issue)
}

// isSquadLeaderReady 是 issue 指派给 squad 时的 leader 就绪检查：只有 leader 的 runtime 在线才算就绪。
func (s *IssueService) isSquadLeaderReady(ctx context.Context, issue db.Issue) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return false
	}
	squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	agent, err := s.Queries.GetAgent(ctx, squad.LeaderID)
	if err != nil {
		return false
	}
	verdict, err := AgentReadiness(ctx, s.runtimeLookup(s.Queries), agent)
	if err != nil {
		return false
	}
	return verdict.Ready()
}

// enqueueSquadLeaderTask 给指派了 squad 的 issue 入队一条 leader 任务（带防重的 head_sha 键）。
func (s *IssueService) enqueueSquadLeaderTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, authorType, authorID string) {
	squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return
	}
	hasPending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: squad.LeaderID,
		// 以被评审的 head 做键去重（TEN-356）。
		HeadSha: headShaText(s.TaskService.ResolveIssueReviewSHA(ctx, issue.ID)),
	})
	if err != nil || hasPending {
		return
	}
	if _, err := s.TaskService.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, squad.ID, triggerCommentID); err != nil {
		slog.Warn("enqueue squad leader task on create failed",
			"issue_id", util.UUIDToString(issue.ID),
			"squad_id", util.UUIDToString(squad.ID),
			"leader_id", util.UUIDToString(squad.LeaderID),
			"error", err)
	}
}
