package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type skillCreateInput struct {
	WorkspaceID pgtype.UUID
	CreatorID   pgtype.UUID
	Name        string
	Description string
	Content     string
	Config      map[string]any
	Files       []CreateSkillFileRequest
	// AllowNameConflict returns pgx.ErrNoRows without aborting the transaction
	// when another skill owns the name. Rename import uses this to try the next
	// deterministic suffix inside one transaction.
	AllowNameConflict bool
}

func upsertSkillFiles(ctx context.Context, q *db.Queries, skillID pgtype.UUID, files []CreateSkillFileRequest) ([]SkillFileResponse, error) {
	responses := make([]SkillFileResponse, 0, len(files))
	for _, file := range files {
		// SKILL.md is the primary skill content stored on skill.Content. Files
		// contains only supporting assets across create, update and import.
		if skillpkg.IsReservedContentPath(file.Path) {
			continue
		}
		stored, err := q.UpsertSkillFile(ctx, db.UpsertSkillFileParams{
			SkillID: skillID,
			Path:    sanitizePostgresText(file.Path),
			Content: sanitizePostgresText(file.Content),
		})
		if err != nil {
			return nil, err
		}
		responses = append(responses, skillFileToResponse(stored))
	}
	return responses, nil
}

func marshalSkillConfig(config map[string]any) ([]byte, error) {
	if config == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(config)
}

func storeSkillFilesResponse(ctx context.Context, q *db.Queries, skill db.Skill, files []CreateSkillFileRequest) (SkillWithFilesResponse, error) {
	fileResponses, err := upsertSkillFiles(ctx, q, skill.ID, files)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	skillResponse, err := skillToResponse(skill)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	return SkillWithFilesResponse{SkillResponse: skillResponse, Files: fileResponses}, nil
}

// createSkillWithFilesInTx writes a skill plus its supporting files using the
// provided sqlc Queries handle, which must already be bound to an open
// transaction. Callers compose skill creation with other writes (e.g. agent
// template materialization) inside one outer transaction. For standalone
// skill creation, prefer createSkillWithFiles, which manages its own tx.
func createSkillWithFilesInTx(ctx context.Context, qtx *db.Queries, input skillCreateInput) (SkillWithFilesResponse, error) {
	config, err := marshalSkillConfig(input.Config)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}

	params := db.CreateSkillParams{
		WorkspaceID: input.WorkspaceID,
		Name:        sanitizePostgresText(input.Name),
		Description: sanitizePostgresText(input.Description),
		Content:     sanitizePostgresText(input.Content),
		Config:      config,
		CreatedBy:   input.CreatorID,
	}
	var skill db.Skill
	if input.AllowNameConflict {
		skill, err = qtx.CreateSkillIfNameAvailable(ctx, db.CreateSkillIfNameAvailableParams(params))
	} else {
		skill, err = qtx.CreateSkill(ctx, params)
	}
	if err != nil {
		return SkillWithFilesResponse{}, err
	}

	return storeSkillFilesResponse(ctx, qtx, skill, input.Files)
}

func (h *Handler) createSkillWithFiles(ctx context.Context, input skillCreateInput) (SkillWithFilesResponse, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.Queries.WithTx(tx)

	result, err := createSkillWithFilesInTx(ctx, qtx, input)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return SkillWithFilesResponse{}, err
	}

	return result, nil
}

// errSkillOverwriteNotFound / errSkillOverwriteForbidden are the terminal
// boundary cases of overwriteSkillWithFiles: the target was deleted (or moved
// out of the workspace) or the caller lost overwrite permission between the
// user's confirm and this write. Callers map them to a failed import and must
// NOT fall back to creating a new skill.
var (
	errSkillOverwriteNotFound     = errors.New("target skill not found")
	errSkillOverwriteForbidden    = errors.New("not permitted to overwrite target skill")
	errSkillOverwriteNameMismatch = errors.New("target skill name does not match the imported skill")
)

type skillOverwriteInput struct {
	WorkspaceID   pgtype.UUID
	TargetSkillID pgtype.UUID
	UserID        string // re-checked against the skill creator inside the tx
	// ExpectedName, when non-empty, must equal the target's current name. Guards
	// against a client sending the wrong target_skill_id and overwriting a
	// different skill than the one the conflict dialog showed the user. The
	// caller passes the sanitized effective import name.
	ExpectedName string
	Description  string
	Content      string
	Config       map[string]any
	Files        []CreateSkillFileRequest
}

// overwriteSkillWithFiles re-imports a bundle onto an existing skill in a single
// transaction. It re-verifies, inside that tx, that the target still exists in
// the workspace and that UserID may overwrite it (creator-only — see
// canOverwriteSkillByLocalImport). A target deleted or a creator change between
// the user's confirm and this write fails cleanly via errSkillOverwriteNotFound
// / errSkillOverwriteForbidden rather than falling back to create.
//
// Preserved: id, created_by, created_at, name, and agent_skill bindings (the
// row identity and the binding table are never touched). Replaced: description,
// content, config (origin), and the full file set — files absent from the new
// bundle are pruned via DeleteSkillFilesBySkill. On any error the tx rolls back,
// leaving the original skill unchanged.
func overwriteSkillWithFilesInTx(ctx context.Context, qtx *db.Queries, input skillOverwriteInput) (SkillWithFilesResponse, error) {
	config, err := marshalSkillConfig(input.Config)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}

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
	if !canOverwriteSkillByLocalImport(input.UserID, existing) {
		return SkillWithFilesResponse{}, errSkillOverwriteForbidden
	}
	// The overwrite is keyed on target_skill_id, but the conflict the user
	// confirmed was a same-name collision; reject if the target's name no longer
	// matches the imported skill so a stale/wrong target_skill_id can't write
	// one skill's content onto another.
	if input.ExpectedName != "" && existing.Name != input.ExpectedName {
		return SkillWithFilesResponse{}, errSkillOverwriteNameMismatch
	}

	// Name is intentionally left unset (COALESCE keeps the existing name): the
	// overwrite targets the same-name skill, so preserving it avoids any
	// unique-name churn.
	skill, err := qtx.UpdateSkill(ctx, db.UpdateSkillParams{
		ID:          existing.ID,
		Description: pgtype.Text{String: sanitizePostgresText(input.Description), Valid: true},
		Content:     pgtype.Text{String: sanitizePostgresText(input.Content), Valid: true},
		Config:      config,
	})
	if err != nil {
		// A committed concurrent DELETE can land between the read above and this
		// UPDATE (READ COMMITTED), so UpdateSkill matches 0 rows. Classify it as
		// the same "target gone" terminal case rather than a generic failure.
		if errors.Is(err, pgx.ErrNoRows) {
			return SkillWithFilesResponse{}, errSkillOverwriteNotFound
		}
		return SkillWithFilesResponse{}, err
	}

	// Full replace: drop every existing file, then re-insert the new set so
	// files no longer present in the local source are removed.
	if err := qtx.DeleteSkillFilesBySkill(ctx, skill.ID); err != nil {
		return SkillWithFilesResponse{}, err
	}
	return storeSkillFilesResponse(ctx, qtx, skill, input.Files)
}

func (h *Handler) overwriteSkillWithFiles(ctx context.Context, input skillOverwriteInput) (SkillWithFilesResponse, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result, err := overwriteSkillWithFilesInTx(ctx, h.Queries.WithTx(tx), input)
	if err != nil {
		return SkillWithFilesResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SkillWithFilesResponse{}, err
	}
	return result, nil
}
