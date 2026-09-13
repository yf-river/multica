// 本文件实现 agent 调用权限模型（MUL-3963）的解析与落库：permission_mode
// （private / public_to）+ invocation_targets（workspace/member/team 白名单）的
// 线格式 DTO、规范化解析、整体替换写入，以及为兼容旧客户端而派生的遗留
// visibility 字段。还负责判定一次 update 请求是否会实际改变权限（让非所有者的
// no-op 回灌通过、真变更以 403 拒绝）。
package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AgentInvocationTargetDTO 是白名单（调用允许列表）中一项的线格式（MUL-3963）。
// 客户端省略的 team 占位行 target_id 为 null；后端持久化的 workspace（工作区 id）
// 与 member（用户 id）行则总是有值。
type AgentInvocationTargetDTO struct {
	TargetType string  `json:"target_type"`
	TargetID   *string `json:"target_id"`
}

const (
	permissionModePrivate  = "private"
	permissionModePublicTo = "public_to"

	invocationTargetWorkspace = "workspace"
	invocationTargetMember    = "member"
	invocationTargetTeam      = "team"
)

// deriveLegacyVisibility 把权限模型映射回遗留的两值 visibility 字段，让旧客户端
// 永远不会观察到权限被悄悄放宽：
//   - public_to 且含 workspace target → "workspace"（人人可调用）
//   - 其它一切（private，或 public_to 只限 member/team）→ "private"
func deriveLegacyVisibility(permissionMode string, targets []db.AgentInvocationTarget) string {
	if permissionMode == permissionModePublicTo {
		for _, t := range targets {
			if t.TargetType == invocationTargetWorkspace {
				return "workspace"
			}
		}
	}
	return "private"
}

// applyInvocationTargetsToResponse 填充 InvocationTargets，并用已加载的 targets
// 重算派生的遗留 Visibility，让权限的两个视图在同一处保持一致。
func applyInvocationTargetsToResponse(resp *AgentResponse, targets []db.AgentInvocationTarget) {
	dto := make([]AgentInvocationTargetDTO, 0, len(targets))
	for _, t := range targets {
		var idPtr *string
		if t.TargetID.Valid {
			s := uuidToString(t.TargetID)
			idPtr = &s
		}
		dto = append(dto, AgentInvocationTargetDTO{TargetType: t.TargetType, TargetID: idPtr})
	}
	resp.InvocationTargets = dto
	resp.Visibility = deriveLegacyVisibility(resp.PermissionMode, targets)
}

// resolvedPermission 是解析 create/update 请求上权限字段（或遗留 visibility）
// 得到的规范化结果。
type resolvedPermission struct {
	mode    string
	targets []targetSpec
}

// targetSpec 是解析后的单个白名单目标。team 占位行的 targetID 无效。
type targetSpec struct {
	targetType string
	targetID   pgtype.UUID // invalid for team placeholders
}

// legacyVisibility 是该权限映射到的、为向后兼容保持同步的 visibility 列值。
func (p resolvedPermission) legacyVisibility() string {
	if p.mode == permissionModePublicTo {
		for _, t := range p.targets {
			if t.targetType == invocationTargetWorkspace {
				return "workspace"
			}
		}
	}
	return "private"
}

// parsePermissionInput 规范化一对 permission_mode + invocation_targets；新字段
// 缺席时回退到遗留 visibility 值。
//
//   - permissionMode == nil && visibility == nil  → 调用方默认（返回 ok=false, nil）
//   - 提供了 permissionMode                     → 权威
//   - 只提供遗留 visibility                     → 映射：
//     "private"   → private
//     "workspace" → public_to + workspace target
//
// workspaceID 为 workspace target 播种（存成工作区 id）。返回的 error 是面向
// 客户端的 400 文案。
func parsePermissionInput(workspaceID pgtype.UUID, permissionMode *string, targets []AgentInvocationTargetDTO, hasPermissionMode, hasTargets bool, legacyVisibility *string) (resolvedPermission, bool, error) {
	if !hasPermissionMode && legacyVisibility == nil {
		return resolvedPermission{}, false, nil
	}

	// 仅遗留路径：把 visibility 映射到新模型。
	if !hasPermissionMode {
		switch *legacyVisibility {
		case "workspace":
			return resolvedPermission{
				mode:    permissionModePublicTo,
				targets: []targetSpec{{targetType: invocationTargetWorkspace, targetID: workspaceID}},
			}, true, nil
		case "private", "":
			return resolvedPermission{mode: permissionModePrivate}, true, nil
		default:
			return resolvedPermission{}, false, fmt.Errorf("visibility must be 'private' or 'workspace'")
		}
	}

	mode := permissionModePrivate
	if permissionMode != nil && *permissionMode != "" {
		mode = *permissionMode
	}
	if mode != permissionModePrivate && mode != permissionModePublicTo {
		return resolvedPermission{}, false, fmt.Errorf("permission_mode must be 'private' or 'public_to'")
	}

	res := resolvedPermission{mode: mode}
	if mode == permissionModePrivate {
		// private 忽略任何提交的 targets：默认拒绝。
		return res, true, nil
	}

	// public_to：规范化目标列表——去重并校验。
	if hasTargets {
		seen := map[string]struct{}{}
		for _, t := range targets {
			switch t.TargetType {
			case invocationTargetWorkspace:
				key := "workspace"
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				res.targets = append(res.targets, targetSpec{targetType: invocationTargetWorkspace, targetID: workspaceID})
			case invocationTargetMember:
				if t.TargetID == nil || *t.TargetID == "" {
					return resolvedPermission{}, false, fmt.Errorf("member invocation target requires target_id")
				}
				uid, err := util.ParseUUID(*t.TargetID)
				if err != nil {
					return resolvedPermission{}, false, fmt.Errorf("member invocation target_id is not a valid uuid")
				}
				key := "member:" + *t.TargetID
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				res.targets = append(res.targets, targetSpec{targetType: invocationTargetMember, targetID: uid})
			case invocationTargetTeam:
				if t.TargetID == nil || *t.TargetID == "" {
					return resolvedPermission{}, false, fmt.Errorf("team invocation target requires target_id")
				}
				tid, err := util.ParseUUID(*t.TargetID)
				if err != nil {
					return resolvedPermission{}, false, fmt.Errorf("team invocation target_id is not a valid uuid")
				}
				key := "team:" + *t.TargetID
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				res.targets = append(res.targets, targetSpec{targetType: invocationTargetTeam, targetID: tid})
			default:
				return resolvedPermission{}, false, fmt.Errorf("invocation target_type must be 'workspace', 'member', or 'team'")
			}
		}
	}
	// 空的 public_to 是幽灵状态：「谁都没共享」，前端会渲染成 workspace 共享而
	// 后端谁也不收。按 MUL-3963 裁定，public_to 没有任何可解析目标时规范化为单个
	// workspace target——这也让不带任何 --public-to-* 旗标的
	// `--permission-mode public_to` 表示「向 workspace 公开」。
	if len(res.targets) == 0 {
		res.targets = append(res.targets, targetSpec{targetType: invocationTargetWorkspace, targetID: workspaceID})
	}
	return res, true, nil
}

// replaceInvocationTargets 整体重写 agent 的调用白名单：清空再重插。在 agent 行
// 已存在后的 create/update 内调用。
func (h *Handler) replaceInvocationTargets(ctx context.Context, agentID pgtype.UUID, createdBy pgtype.UUID, targets []targetSpec) error {
	return replaceInvocationTargetsWithQueries(ctx, h.Queries, agentID, createdBy, targets)
}

// replaceInvocationTargetsWithQueries 是事务友好变体：持有
// `qtx := h.Queries.WithTx(tx)` 的调用方可把 qtx 传进来，让 invocation target 行
// 与 agent 行写在同一事务里。全新的 agent 行绝不能处于「行存在但 targets 缺失」
// 的状态。
func replaceInvocationTargetsWithQueries(ctx context.Context, q *db.Queries, agentID pgtype.UUID, createdBy pgtype.UUID, targets []targetSpec) error {
	if err := q.DeleteAgentInvocationTargets(ctx, agentID); err != nil {
		return err
	}
	for _, t := range targets {
		if err := q.CreateAgentInvocationTarget(ctx, db.CreateAgentInvocationTargetParams{
			AgentID:    agentID,
			TargetType: t.targetType,
			TargetID:   t.targetID,
			CreatedBy:  createdBy,
		}); err != nil {
			return err
		}
	}
	return nil
}

// permissionInputChangesAgent 判断一次 update 请求上的权限字段是否真的会改变
// agent 当前的调用权限（模式或目标集合）。用于让非所有者的 no-op 回灌通过
// （PATCH-as-PUT 原样回显未变的权限），而真变更以 403 拒绝。非法/缺席的权限输入
// 计为「无变更」——非所有者发来垃圾也不该吃 403，直接忽略即可。失败安全：DB
// 出错时返回 changed=true，让非所有者的尝试被拒绝而不是被悄悄应用。
func (h *Handler) permissionInputChangesAgent(ctx context.Context, existing db.Agent, req UpdateAgentRequest, hasPermissionMode, hasTargets bool) (bool, error) {
	// 仅遗留请求：调用者只发了 `visibility`，没有 permission_mode 也没有
	// invocation_targets（旧客户端 / 编辑其它字段时 PATCH-as-PUT 原样回显该字段）。
	// 按「派生的遗留 visibility」比较，而不是把 "private" 展开成真实 private 权限。
	// member-only 的 public_to agent 派生为遗留 "private"，因此 admin 重发
	// visibility:"private" 是 NO-OP，不是 public_to→private 降级。只有真实遗留
	// 变更（如 "workspace"）才算。（MUL-3963 评审——这是 PR #4853 的兼容修复。）
	if !hasPermissionMode && !hasTargets {
		if req.Visibility == nil {
			return false, nil
		}
		current, err := h.Queries.ListAgentInvocationTargets(ctx, existing.ID)
		if err != nil {
			return true, err
		}
		submitted := "private"
		if *req.Visibility == "workspace" {
			submitted = "workspace"
		}
		return submitted != deriveLegacyVisibility(existing.PermissionMode, current), nil
	}

	var targetsDTO []AgentInvocationTargetDTO
	if req.InvocationTargets != nil {
		targetsDTO = *req.InvocationTargets
	}
	perm, ok, err := parsePermissionInput(existing.WorkspaceID, req.PermissionMode, targetsDTO, hasPermissionMode, hasTargets, req.Visibility)
	if err != nil || !ok {
		// 解析不了或实际上没有权限字段 → 视为无变更。
		return false, nil
	}
	if perm.mode != existing.PermissionMode {
		return true, nil
	}
	current, err := h.Queries.ListAgentInvocationTargets(ctx, existing.ID)
	if err != nil {
		return true, err
	}
	want := make(map[string]struct{}, len(perm.targets))
	for _, tgt := range perm.targets {
		want[tgt.targetType+":"+uuidToString(tgt.targetID)] = struct{}{}
	}
	have := make(map[string]struct{}, len(current))
	for _, row := range current {
		have[row.TargetType+":"+uuidToString(row.TargetID)] = struct{}{}
	}
	if len(want) != len(have) {
		return true, nil
	}
	for k := range want {
		if _, ok := have[k]; !ok {
			return true, nil
		}
	}
	return false, nil
}

// enrichAgentResponseWithTargets 加载 agent 的 invocation targets 并应用到响应
// （InvocationTargets + 派生 Visibility）。供单 agent 的 detail / create / update
// 响应使用。
func (h *Handler) enrichAgentResponseWithTargets(ctx context.Context, resp *AgentResponse, agentID pgtype.UUID) error {
	targets, err := h.Queries.ListAgentInvocationTargets(ctx, agentID)
	if err != nil {
		return err
	}
	applyInvocationTargetsToResponse(resp, targets)
	return nil
}

// enrichAgentResponseWithTargetsHTTP 是 HTTP 边界包装：失败时写好 500 并返回
// false。
func (h *Handler) enrichAgentResponseWithTargetsHTTP(w http.ResponseWriter, r *http.Request, resp *AgentResponse, agentID pgtype.UUID) bool {
	if err := h.enrichAgentResponseWithTargets(r.Context(), resp, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return false
	}
	return true
}
