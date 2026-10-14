// 本文件是技能创建的底层原语：createSkillWithFiles（自带事务的独立创建）与
// overwriteSkillWithFiles（单事务内把资源包「重导入」到既有技能上）。两者都落
// skill:created / skill:updated 域事件。覆盖路径在锁内复查目标技能仍存在、调用者
// 仍有覆盖权（默认仅创建者），并把错误分类为 NOT_FOUND / FORBIDDEN /
// NAME_MISMATCH / NAME_CONFLICT，绝不回退成新建。
package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// skillCreateInput 是一次技能创建所需的全部入参（不含事务）。
type skillCreateInput struct {
	WorkspaceID pgtype.UUID
	CreatorID   pgtype.UUID
	Name        string
	Description string
	Content     string
	Config      any
	Files       []CreateSkillFileRequest
}

// createSkillWithFilesInTx 用给定的 sqlc Queries 句柄写一个技能及其附带文件，该
// 句柄必须已绑定到一个打开的事务上。调用方把技能创建与其它写入（例如 agent
// 模板落地）组合进同一个外层事务。独立创建技能请用自管事务的
// createSkillWithFiles。
func createSkillWithFilesInTx(ctx context.Context, qtx *db.Queries, input skillCreateInput) (SkillWithFilesResponse, error) {
	config, err := json.Marshal(input.Config)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	if input.Config == nil {
		config = []byte("{}")
	}

	skill, err := qtx.CreateSkill(ctx, db.CreateSkillParams{
		WorkspaceID: input.WorkspaceID,
		Name:        sanitizeNullBytes(input.Name),
		Description: sanitizeNullBytes(input.Description),
		Content:     sanitizeNullBytes(input.Content),
		Config:      config,
		CreatedBy:   input.CreatorID,
	})
	if err != nil {
		return SkillWithFilesResponse{}, err
	}

	fileResps := make([]SkillFileResponse, 0, len(input.Files))
	for _, f := range input.Files {
		// SKILL.md 预留给主技能正文（skill.Content）。附带文件必须承载额外素材，
		// 而不是复制主文件。
		if skillpkg.IsReservedContentPath(f.Path) {
			continue
		}
		sf, err := upsertPreparedSkillFile(ctx, qtx, skill.ID, f)
		if err != nil {
			return SkillWithFilesResponse{}, err
		}
		fileResps = append(fileResps, skillFileToResponse(sf))
	}

	return SkillWithFilesResponse{
		SkillResponse: skillToResponse(skill),
		Files:         fileResps,
	}, nil
}

func (h *Handler) createSkillWithFiles(ctx context.Context, input skillCreateInput) (SkillWithFilesResponse, error) {
	if err := h.prepareSkillFiles(ctx, input.WorkspaceID, pgtype.UUID{}, input.Files); err != nil {
		return SkillWithFilesResponse{}, err
	}
	// 独立创建的默认入口：自管事务，成功则广播 skill:created 事件。
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)

	result, err := createSkillWithFilesInTx(ctx, qtx, input)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}

	skill, err := qtx.GetSkillInWorkspace(ctx, db.GetSkillInWorkspaceParams{ID: parseUUID(result.ID), WorkspaceID: input.WorkspaceID})
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	event, err := service.RecordDurableEventTx(ctx, qtx, events.Event{
		Type:           protocol.EventSkillCreated,
		IdempotencyKey: "skill:created:" + uuidToString(skill.ID),
		StreamKey:      "skill:" + uuidToString(skill.ID), WorkspaceID: uuidToString(skill.WorkspaceID),
		ActorType: "member", ActorID: uuidToString(input.CreatorID),
		Payload: map[string]any{"skill": result},
	})
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SkillWithFilesResponse{}, err
	}
	h.publishEvent(event)

	return result, nil
}

// errSkillOverwriteNotFound / errSkillOverwriteForbidden 是
// overwriteSkillWithFiles 的终态边界：目标被删除（或移出工作区）、或调用者在这
// 次写入前丢掉了覆盖权。调用方把它们映射为导入失败，绝不能回退去新建技能。
var (
	errSkillOverwriteNotFound     = errors.New("target skill not found")
	errSkillOverwriteForbidden    = errors.New("not permitted to overwrite target skill")
	errSkillOverwriteNameMismatch = errors.New("target skill name does not match the imported skill")
	errSkillOverwriteNameConflict = errors.New("another skill in the workspace already has the imported name")
)

// skillOverwriteInput 是一次「覆盖既有技能」所需的全部入参。
type skillOverwriteInput struct {
	WorkspaceID   pgtype.UUID
	TargetSkillID pgtype.UUID
	UserID        string // re-checked against the skill creator inside the tx
	// ExpectedName 非空时必须等于目标当前名。防止客户端发错 target_skill_id、把
	// 与冲突对话框展示的技能不同的那个给覆盖了。调用方传入清洗后的有效导入名。
	ExpectedName string
	// NewName 非空时把目标改名为导入技能的名字。与工作区内另一技能撞名
	// （UNIQUE(workspace_id, name)）会让整个覆盖以 errSkillOverwriteNameConflict
	// 失败。
	NewName string
	// AllowOverwrite 覆盖事务内的权限复查。为 nil 时保持默认的仅创建者策略
	// （canOverwriteSkillByLocalImport）。
	AllowOverwrite func(userID string, skill db.Skill) bool
	Description    string
	Content        string
	Config         any
	Files          []CreateSkillFileRequest
}

// overwriteSkillWithFiles 在单事务内把资源包重新导入到既有技能上。它在该事务
// 内复查目标仍存在于工作区、且 UserID 有权覆盖（仅创建者——见
// canOverwriteSkillByLocalImport）。若用户确认到本次写入之间目标被删或创建者
// 变了，则以 errSkillOverwriteNotFound / errSkillOverwriteForbidden 干净失败，
// 而不是回退去新建。
//
// 保留：id、created_by、created_at、name 与 agent_skill 绑定（行身份与绑定表
// 从不触碰）。替换：description、content、config（origin）与整个文件集合——新
// 包里缺席的文件经 DeleteSkillFilesBySkill 修剪。任何错误都回滚事务，原技能保持
// 不变。
func (h *Handler) overwriteSkillWithFiles(ctx context.Context, input skillOverwriteInput) (SkillWithFilesResponse, error) {
	if err := h.prepareSkillFiles(ctx, input.WorkspaceID, input.TargetSkillID, input.Files); err != nil {
		return SkillWithFilesResponse{}, err
	}
	config, err := json.Marshal(input.Config)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	if input.Config == nil {
		config = []byte("{}")
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)

	existing, err := qtx.GetSkillInWorkspace(ctx, db.GetSkillInWorkspaceParams{
		ID:          input.TargetSkillID,
		WorkspaceID: input.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SkillWithFilesResponse{}, errSkillOverwriteNotFound
		}
		return SkillWithFilesResponse{}, err
	}
	allowOverwrite := input.AllowOverwrite
	if allowOverwrite == nil {
		allowOverwrite = canOverwriteSkillByLocalImport
	}
	if !allowOverwrite(input.UserID, existing) {
		return SkillWithFilesResponse{}, errSkillOverwriteForbidden
	}
	// 覆盖以 target_skill_id 为键，但用户确认的冲突是同名碰撞；若目标名已不再
	// 匹配导入技能就拒绝——过期/错误的 target_skill_id 不能把一个技能的内容写进
	// 另一个。
	if input.ExpectedName != "" && existing.Name != input.ExpectedName {
		return SkillWithFilesResponse{}, errSkillOverwriteNameMismatch
	}

	// name 默认保持未设置（COALESCE 保留现有名）：导入冲突覆盖瞄准的就是同名
	// 技能，保留它避免任何唯一名抖动。从源头刷新会传 NewName 来采纳上游改名。
	var newName pgtype.Text
	if n := sanitizeNullBytes(input.NewName); n != "" && n != existing.Name {
		newName = pgtype.Text{String: n, Valid: true}
	}
	skill, err := qtx.UpdateSkill(ctx, db.UpdateSkillParams{
		ID:          existing.ID,
		Name:        newName,
		Description: pgtype.Text{String: sanitizeNullBytes(input.Description), Valid: true},
		Content:     pgtype.Text{String: sanitizeNullBytes(input.Content), Valid: true},
		Config:      config,
	})
	if err != nil {
		// 已提交的并发 DELETE 可能落在上面的读与本次 UPDATE 之间（READ
		// COMMITTED），使 UpdateSkill 匹配 0 行。把它归类为同样的「目标已消失」
		// 终态，而不是笼统失败。
		if errors.Is(err, pgx.ErrNoRows) {
			return SkillWithFilesResponse{}, errSkillOverwriteNotFound
		}
		if newName.Valid && isUniqueViolation(err) {
			return SkillWithFilesResponse{}, errSkillOverwriteNameConflict
		}
		return SkillWithFilesResponse{}, err
	}

	// 整体替换：先清空全部既有文件，再重插新集合——本地源中已缺席的文件随之
	// 移除。
	if err := qtx.DeleteSkillFilesBySkill(ctx, skill.ID); err != nil {
		return SkillWithFilesResponse{}, err
	}
	fileResps := make([]SkillFileResponse, 0, len(input.Files))
	for _, f := range input.Files {
		sf, err := upsertPreparedSkillFile(ctx, qtx, skill.ID, f)
		if err != nil {
			return SkillWithFilesResponse{}, err
		}
		fileResps = append(fileResps, skillFileToResponse(sf))
	}
	event, err := service.RecordDurableEventTx(ctx, qtx, events.Event{
		Type:           protocol.EventSkillUpdated,
		IdempotencyKey: "skill:updated:" + uuidToString(skill.ID) + ":" + skill.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "skill:" + uuidToString(skill.ID), WorkspaceID: uuidToString(skill.WorkspaceID),
		ActorType: "member", ActorID: input.UserID,
		Payload: map[string]any{"skill": SkillWithFilesResponse{SkillResponse: skillToResponse(skill), Files: fileResps}},
	})
	if err != nil {
		return SkillWithFilesResponse{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return SkillWithFilesResponse{}, err
	}
	h.publishEvent(event)
	result := SkillWithFilesResponse{
		SkillResponse: skillToResponse(skill),
		Files:         fileResps,
	}
	return result, nil
}
