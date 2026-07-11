package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	promptEvaluationSkillInventorySchema = "multica.skill.inventory.v1"
	promptEvaluationSkillSnapshotSchema  = "multica.skill.snapshot.v1"
	promptEvaluationSkillCaseDraftSchema = "multica.skill.case_draft.v1"
	promptEvaluationSkillPatchSchema     = "multica.skill.patch.v1"
	promptEvaluationSkillFreshnessSchema = "multica.skill.freshness.v1"
	promptEvaluationSkillApplySchema     = "multica.skill.apply.v1"
)

type CreatePromptEvaluationSkillInventoryRequest struct {
	Provider         string `json:"provider"`
	Repo             string `json:"repo"`
	RepoPath         string `json:"repo_path"`
	Branch           string `json:"branch"`
	SkillRoot        string `json:"skill_root"`
	SourceResourceID string `json:"source_resource_id"`
}

type PromptEvaluationSkillInventoryItem struct {
	SkillPath         string `json:"skill_path"`
	SkillName         string `json:"skill_name"`
	SkillHash         string `json:"skill_hash"`
	LastCommit        string `json:"last_commit,omitempty"`
	LastCommitSubject string `json:"last_commit_subject,omitempty"`
	LastUpdatedAt     string `json:"last_updated_at,omitempty"`
	ChangelogPath     string `json:"changelog_path,omitempty"`
	HasChangelog      bool   `json:"has_changelog"`
	Tracked           bool   `json:"tracked"`
}

type PromptEvaluationSkillInventoryResult struct {
	SchemaVersion    string                               `json:"schema_version"`
	Provider         string                               `json:"provider"`
	Repo             string                               `json:"repo"`
	RepoPath         string                               `json:"repo_path,omitempty"`
	Branch           string                               `json:"branch"`
	HeadCommit       string                               `json:"head_commit"`
	SkillRoot        string                               `json:"skill_root"`
	Items            []PromptEvaluationSkillInventoryItem `json:"items"`
	DiscoveredCount  int                                  `json:"discovered_count"`
	SnapshotTime     string                               `json:"snapshot_time"`
	SourceResourceID string                               `json:"source_resource_id,omitempty"`
}

type PromptEvaluationSkillInventoryResponse struct {
	Asset     PromptEvaluationAssetResponse        `json:"asset"`
	Inventory PromptEvaluationSkillInventoryResult `json:"inventory"`
}

type CreatePromptEvaluationSkillSnapshotRequest struct {
	Provider         string `json:"provider"`
	Repo             string `json:"repo"`
	RepoPath         string `json:"repo_path"`
	Branch           string `json:"branch"`
	SkillPath        string `json:"skill_path"`
	SourceResourceID string `json:"source_resource_id"`
}

type PromptEvaluationSkillSnapshotResponse struct {
	SchemaVersion    string `json:"schema_version"`
	Provider         string `json:"provider"`
	Repo             string `json:"repo"`
	RepoPath         string `json:"repo_path,omitempty"`
	Branch           string `json:"branch"`
	BaseCommit       string `json:"base_commit"`
	SkillPath        string `json:"skill_path"`
	SkillHash        string `json:"skill_hash"`
	SnapshotTime     string `json:"snapshot_time"`
	SourceResourceID string `json:"source_resource_id,omitempty"`
}

type PromptEvaluationSkillSnapshotResult struct {
	Asset    PromptEvaluationAssetResponse         `json:"asset"`
	Snapshot PromptEvaluationSkillSnapshotResponse `json:"snapshot"`
}

type PromptEvaluationSkillPatch struct {
	SchemaVersion        string                                 `json:"schema_version"`
	Patch                string                                 `json:"patch"`
	PatchHash            string                                 `json:"patch_hash"`
	PatchBytes           int                                    `json:"patch_bytes"`
	CandidateIntent      string                                 `json:"candidate_intent,omitempty"`
	OperationSkillKey    string                                 `json:"operation_skill_key,omitempty"`
	OperationSkillPath   string                                 `json:"operation_skill_path,omitempty"`
	OperationSkillReason string                                 `json:"operation_skill_reason,omitempty"`
	SourceSnapshot       *PromptEvaluationSkillSnapshotResponse `json:"source_snapshot,omitempty"`
	SourceResourceID     string                                 `json:"source_resource_id,omitempty"`
	RepoPath             string                                 `json:"repo_path,omitempty"`
	TargetBranch         string                                 `json:"target_branch,omitempty"`
	SkillPath            string                                 `json:"skill_path,omitempty"`
	ChangelogPath        string                                 `json:"changelog_path,omitempty"`
	ExpectedImprovement  string                                 `json:"expected_improvement,omitempty"`
	Risk                 string                                 `json:"risk,omitempty"`
	VerificationPlan     string                                 `json:"verification_plan,omitempty"`
	PublicationStatus    string                                 `json:"publication_status"`
	CreatedAt            string                                 `json:"created_at,omitempty"`
	UpdatedAt            string                                 `json:"updated_at,omitempty"`
}

type CreatePromptEvaluationSkillCaseDraftsRequest struct {
	RepoPath    string `json:"repo_path"`
	SkillPath   string `json:"skill_path"`
	Limit       int    `json:"limit"`
	AutoApprove bool   `json:"auto_approve"`
}

type PromptEvaluationSkillCaseDraft struct {
	SchemaVersion       string `json:"schema_version"`
	Status              string `json:"status"`
	Input               string `json:"input"`
	ExpectedBehavior    string `json:"expected_behavior"`
	Verification        string `json:"verification"`
	EvidenceSource      string `json:"evidence_source"`
	ApplicableSkillHash string `json:"applicable_skill_hash,omitempty"`
	ApplicableScope     string `json:"applicable_scope"`
	SourceCommit        string `json:"source_commit"`
	CommitSubject       string `json:"commit_subject"`
	SkillPath           string `json:"skill_path"`
	BeforeHash          string `json:"before_hash,omitempty"`
	AfterHash           string `json:"after_hash,omitempty"`
}

type PromptEvaluationSkillCaseDraftsResult struct {
	Asset        PromptEvaluationAssetResponse    `json:"asset"`
	Drafts       []PromptEvaluationSkillCaseDraft `json:"drafts"`
	CreatedCount int                              `json:"created_count"`
}

type CheckPromptEvaluationSkillFreshnessRequest struct {
	SourceResourceID string                                 `json:"source_resource_id"`
	RepoPath         string                                 `json:"repo_path"`
	TargetBranch     string                                 `json:"target_branch"`
	SkillPath        string                                 `json:"skill_path"`
	CandidatePatch   string                                 `json:"candidate_patch"`
	CandidateIntent  string                                 `json:"candidate_intent"`
	Snapshot         *PromptEvaluationSkillSnapshotResponse `json:"snapshot"`
}

type PromptEvaluationSkillFreshnessResult struct {
	SchemaVersion    string                                `json:"schema_version"`
	Status           string                                `json:"status"`
	Reason           string                                `json:"reason"`
	TargetBranch     string                                `json:"target_branch"`
	HeadCommit       string                                `json:"head_commit"`
	BaseCommit       string                                `json:"base_commit"`
	SkillPath        string                                `json:"skill_path"`
	BaseSkillHash    string                                `json:"base_skill_hash"`
	CurrentSkillHash string                                `json:"current_skill_hash"`
	PatchCheck       string                                `json:"patch_check"`
	CheckedAt        string                                `json:"checked_at"`
	Snapshot         PromptEvaluationSkillSnapshotResponse `json:"snapshot"`
}

type ApplyPromptEvaluationSkillCandidateRequest struct {
	SourceResourceID   string                                 `json:"source_resource_id"`
	RepoPath           string                                 `json:"repo_path"`
	TargetBranch       string                                 `json:"target_branch"`
	SkillPath          string                                 `json:"skill_path"`
	CandidatePatch     string                                 `json:"candidate_patch"`
	CandidateIntent    string                                 `json:"candidate_intent"`
	ChangelogPath      string                                 `json:"changelog_path"`
	ChangeReason       string                                 `json:"change_reason"`
	VerificationResult string                                 `json:"verification_result"`
	RollbackPlan       string                                 `json:"rollback_plan"`
	DryRun             bool                                   `json:"dry_run"`
	AllowDirty         bool                                   `json:"allow_dirty"`
	SkipChangelog      bool                                   `json:"skip_changelog"`
	Snapshot           *PromptEvaluationSkillSnapshotResponse `json:"snapshot"`
}

type PromptEvaluationSkillApplyResult struct {
	SchemaVersion   string                                `json:"schema_version"`
	Status          string                                `json:"status"`
	Reason          string                                `json:"reason"`
	RepoPath        string                                `json:"repo_path"`
	TargetBranch    string                                `json:"target_branch"`
	HeadCommit      string                                `json:"head_commit"`
	SkillPath       string                                `json:"skill_path"`
	SkillHashBefore string                                `json:"skill_hash_before"`
	SkillHashAfter  string                                `json:"skill_hash_after"`
	ChangelogPath   string                                `json:"changelog_path,omitempty"`
	PatchCheck      string                                `json:"patch_check"`
	Freshness       PromptEvaluationSkillFreshnessResult  `json:"freshness"`
	ChangedFiles    []string                              `json:"changed_files"`
	ReEvalRequired  bool                                  `json:"re_eval_required"`
	ReEvalPlan      map[string]any                        `json:"re_eval_plan"`
	CheckedAt       string                                `json:"checked_at"`
	AppliedAt       string                                `json:"applied_at,omitempty"`
	Snapshot        PromptEvaluationSkillSnapshotResponse `json:"snapshot"`
}

type PromptEvaluationSkillApplyCandidateResponse struct {
	Candidate PromptEvaluationOptimizationCandidateResponse `json:"candidate"`
	Apply     PromptEvaluationSkillApplyResult              `json:"apply"`
}

type PreparePromptEvaluationSkillReEvalRequest struct {
	SourceResourceID string                                 `json:"source_resource_id"`
	RepoPath         string                                 `json:"repo_path"`
	TargetBranch     string                                 `json:"target_branch"`
	SkillPath        string                                 `json:"skill_path"`
	Name             string                                 `json:"name"`
	Description      string                                 `json:"description"`
	Statuses         []string                               `json:"statuses"`
	IncludeDraft     bool                                   `json:"include_draft"`
	Snapshot         *PromptEvaluationSkillSnapshotResponse `json:"snapshot"`
}

type RunPromptEvaluationSkillReEvalRequest struct {
	AssetID string `json:"asset_id"`
}

type PromptEvaluationSkillReEvalCase struct {
	Name             string         `json:"name"`
	Variables        map[string]any `json:"variables"`
	ExpectedContains []string       `json:"expected_contains"`
	Tags             []string       `json:"tags"`
	Input            map[string]any `json:"input"`
	Expected         map[string]any `json:"expected"`
	SourceCommit     string         `json:"source_commit"`
	EvidenceSource   string         `json:"evidence_source"`
	Status           string         `json:"status"`
}

type PromptEvaluationSkillReEvalAssetResponse struct {
	Candidate      PromptEvaluationOptimizationCandidateResponse `json:"candidate"`
	Asset          PromptEvaluationAssetResponse                 `json:"asset"`
	SourceSnapshot PromptEvaluationSkillSnapshotResponse         `json:"source_snapshot"`
	ReEvalSnapshot PromptEvaluationSkillSnapshotResponse         `json:"re_eval_snapshot"`
	CaseCount      int                                           `json:"case_count"`
	Cases          []PromptEvaluationSkillReEvalCase             `json:"cases"`
	ReEvalPlan     map[string]any                                `json:"re_eval_plan"`
}

type PromptEvaluationSkillReEvalRunResponse struct {
	Candidate      PromptEvaluationOptimizationCandidateResponse `json:"candidate"`
	Asset          PromptEvaluationAssetResponse                 `json:"asset"`
	Run            PromptEvaluationRunResponse                   `json:"run"`
	SourceSnapshot PromptEvaluationSkillSnapshotResponse         `json:"source_snapshot"`
	ReEvalSnapshot PromptEvaluationSkillSnapshotResponse         `json:"re_eval_snapshot"`
	CaseCount      int                                           `json:"case_count"`
	ProofScope     string                                        `json:"proof_scope"`
	ReEvalRun      map[string]any                                `json:"re_eval_run"`
}

type promptEvaluationSkillGongfengResourceRef struct {
	Provider     string `json:"provider"`
	URL          string `json:"url"`
	ProjectPath  string `json:"project_path"`
	ResourceKind string `json:"resource_kind"`
	Ref          string `json:"ref"`
	HeadCommit   string `json:"head_commit"`
	Title        string `json:"title"`
}

type promptEvaluationSkillLocalDirectoryResourceRef struct {
	LocalPath string `json:"local_path"`
	DaemonID  string `json:"daemon_id"`
	Label     string `json:"label"`
}

func (h *Handler) CreatePromptEvaluationSkillInventory(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationSkillInventoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if ok := h.applyPromptEvaluationSkillSourceResourceDefaults(w, r, &req.Provider, &req.Repo, &req.RepoPath, &req.Branch, &req.SourceResourceID); !ok {
		return
	}
	inventory, err := buildPromptEvaluationSkillInventory(req, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := decodePayloadObject(asset.Payload)
	payload["optimization_target"] = "skill"
	payload["skill_inventory"] = inventory
	payload["skill_inventory_contract"] = promptEvaluationSkillInventorySchema
	payload["skill_inventories"] = appendJSONList(payload["skill_inventories"], inventory)
	updated, ok := h.updatePromptEvaluationAssetPayload(w, r, asset, payload)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, PromptEvaluationSkillInventoryResponse{
		Asset:     promptEvaluationAssetToResponse(updated),
		Inventory: inventory,
	})
}

func (h *Handler) CreatePromptEvaluationSkillSnapshot(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationSkillSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if ok := h.applyPromptEvaluationSkillSourceResourceDefaults(w, r, &req.Provider, &req.Repo, &req.RepoPath, &req.Branch, &req.SourceResourceID); !ok {
		return
	}
	snapshot, err := buildPromptEvaluationSkillSnapshot(req, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := decodePayloadObject(asset.Payload)
	payload["optimization_target"] = "skill"
	payload["skill_snapshot"] = snapshot
	payload["skill_snapshot_contract"] = promptEvaluationSkillSnapshotSchema
	payload["skill_snapshots"] = appendJSONList(payload["skill_snapshots"], snapshot)
	updated, ok := h.updatePromptEvaluationAssetPayload(w, r, asset, payload)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, PromptEvaluationSkillSnapshotResult{
		Asset:    promptEvaluationAssetToResponse(updated),
		Snapshot: snapshot,
	})
}

func (h *Handler) CreatePromptEvaluationSkillCaseDrafts(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationSkillCaseDraftsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	drafts, err := buildPromptEvaluationSkillCaseDrafts(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := decodePayloadObject(asset.Payload)
	payload["skill_case_draft_contract"] = promptEvaluationSkillCaseDraftSchema
	payload["skill_case_drafts"] = appendJSONList(payload["skill_case_drafts"], skillCaseDraftsAsAny(drafts)...)
	updated, ok := h.updatePromptEvaluationAssetPayload(w, r, asset, payload)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, PromptEvaluationSkillCaseDraftsResult{
		Asset:        promptEvaluationAssetToResponse(updated),
		Drafts:       drafts,
		CreatedCount: len(drafts),
	})
}

func (h *Handler) CheckPromptEvaluationSkillCandidateFreshness(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	candidateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation optimization candidate id")
	if !ok {
		return
	}
	candidate, err := h.Queries.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation optimization candidate not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load optimization candidate")
		return
	}
	var req CheckPromptEvaluationSkillFreshnessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	skillPatch := skillPatchFromCandidate(candidate)
	applySkillPatchFreshnessDefaults(&req, skillPatch)
	provider, repo := "", ""
	if ok := h.applyPromptEvaluationSkillSourceResourceDefaults(w, r, &provider, &repo, &req.RepoPath, &req.TargetBranch, &req.SourceResourceID); !ok {
		return
	}
	snapshot := req.Snapshot
	if snapshot == nil && skillPatch != nil {
		snapshot = skillPatch.SourceSnapshot
	}
	if snapshot == nil {
		snapshot = skillSnapshotFromCandidate(candidate)
	}
	if snapshot == nil {
		writeError(w, http.StatusBadRequest, "skill snapshot is required")
		return
	}
	if req.SourceResourceID != "" && snapshot.SourceResourceID == "" {
		snapshot.SourceResourceID = req.SourceResourceID
	}
	if req.CandidatePatch == "" {
		req.CandidatePatch = candidate.CandidateContent
	}
	result, err := checkPromptEvaluationSkillFreshness(req, *snapshot, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := h.mergePromptEvaluationOptimizationCandidateMetrics(r.Context(), workspaceUUID, candidateID, map[string]any{
		"skill_freshness": result,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist skill freshness evidence")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) ApplyPromptEvaluationSkillCandidate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	candidateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation optimization candidate id")
	if !ok {
		return
	}
	candidate, err := h.Queries.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation optimization candidate not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation optimization candidate")
		return
	}
	if candidate.Status != "待确认" {
		writeError(w, http.StatusConflict, "only 待确认 optimization candidates can be applied")
		return
	}
	var req ApplyPromptEvaluationSkillCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	skillPatch := skillPatchFromCandidate(candidate)
	applySkillPatchApplyDefaults(&req, skillPatch)
	provider, repo := "", ""
	if ok := h.applyPromptEvaluationSkillSourceResourceDefaults(w, r, &provider, &repo, &req.RepoPath, &req.TargetBranch, &req.SourceResourceID); !ok {
		return
	}
	snapshot := req.Snapshot
	if snapshot == nil && skillPatch != nil {
		snapshot = skillPatch.SourceSnapshot
	}
	if snapshot == nil {
		snapshot = skillSnapshotFromCandidate(candidate)
	}
	if snapshot == nil {
		writeError(w, http.StatusBadRequest, "skill snapshot is required")
		return
	}
	if req.SourceResourceID != "" && snapshot.SourceResourceID == "" {
		snapshot.SourceResourceID = req.SourceResourceID
	}
	if req.CandidatePatch == "" {
		req.CandidatePatch = candidate.CandidateContent
	}
	result, err := applyPromptEvaluationSkillCandidate(req, *snapshot, map[string]any{
		"candidate_id": uuidToString(candidate.ID),
		"asset_id":     uuidToString(candidate.AssetID),
		"run_id":       uuidToString(candidate.RunID),
		"applied_by":   userID,
	}, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := h.mergePromptEvaluationOptimizationCandidateMetrics(r.Context(), workspaceUUID, candidateID, map[string]any{
		"skill_apply":     result,
		"skill_freshness": result.Freshness,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist skill apply evidence")
		return
	}
	writeJSON(w, http.StatusOK, PromptEvaluationSkillApplyCandidateResponse{
		Candidate: promptEvaluationOptimizationCandidateToResponse(updated),
		Apply:     result,
	})
}

func (h *Handler) PreparePromptEvaluationSkillReEvalAsset(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	candidateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation optimization candidate id")
	if !ok {
		return
	}
	candidate, err := h.Queries.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation optimization candidate not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation optimization candidate")
		return
	}
	sourceAsset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{
		ID:          candidate.AssetID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "source prompt evaluation asset not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load source prompt evaluation asset")
		return
	}
	var req PreparePromptEvaluationSkillReEvalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	skillPatch := skillPatchFromCandidate(candidate)
	applySkillPatchReEvalDefaults(&req, skillPatch)
	provider, repo := "", ""
	if ok := h.applyPromptEvaluationSkillSourceResourceDefaults(w, r, &provider, &repo, &req.RepoPath, &req.TargetBranch, &req.SourceResourceID); !ok {
		return
	}
	sourceSnapshot := req.Snapshot
	if sourceSnapshot == nil && skillPatch != nil {
		sourceSnapshot = skillPatch.SourceSnapshot
	}
	if sourceSnapshot == nil {
		sourceSnapshot = skillSnapshotFromCandidate(candidate)
	}
	if sourceSnapshot == nil {
		sourceSnapshot = skillSnapshotFromAsset(sourceAsset)
	}
	if sourceSnapshot == nil {
		writeError(w, http.StatusBadRequest, "skill snapshot is required")
		return
	}
	if req.SourceResourceID != "" && sourceSnapshot.SourceResourceID == "" {
		sourceSnapshot.SourceResourceID = req.SourceResourceID
	}
	var reEvalSnapshot PromptEvaluationSkillSnapshotResponse
	if applyEvidence := skillApplyFromCandidate(candidate); applyEvidence != nil && applyEvidence.Status == "applied" {
		var err error
		reEvalSnapshot, err = buildPromptEvaluationSkillAppliedWorktreeSnapshot(CreatePromptEvaluationSkillSnapshotRequest{
			Provider:         sourceSnapshot.Provider,
			Repo:             sourceSnapshot.Repo,
			RepoPath:         firstNonEmpty(req.RepoPath, applyEvidence.RepoPath, sourceSnapshot.RepoPath),
			Branch:           firstNonEmpty(req.TargetBranch, applyEvidence.TargetBranch, sourceSnapshot.Branch, "HEAD"),
			SkillPath:        firstNonEmpty(req.SkillPath, applyEvidence.SkillPath, sourceSnapshot.SkillPath),
			SourceResourceID: firstNonEmpty(req.SourceResourceID, sourceSnapshot.SourceResourceID),
		}, *applyEvidence, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		var err error
		reEvalSnapshot, err = buildPromptEvaluationSkillSnapshot(CreatePromptEvaluationSkillSnapshotRequest{
			Provider:         sourceSnapshot.Provider,
			Repo:             sourceSnapshot.Repo,
			RepoPath:         firstNonEmpty(req.RepoPath, sourceSnapshot.RepoPath),
			Branch:           firstNonEmpty(req.TargetBranch, sourceSnapshot.Branch, "HEAD"),
			SkillPath:        firstNonEmpty(req.SkillPath, sourceSnapshot.SkillPath),
			SourceResourceID: firstNonEmpty(req.SourceResourceID, sourceSnapshot.SourceResourceID),
		}, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	drafts := skillCaseDraftsFromAsset(sourceAsset)
	cases := buildPromptEvaluationSkillReEvalCases(drafts, reEvalSnapshot, req)
	if len(cases) == 0 {
		writeError(w, http.StatusBadRequest, "no eligible skill case drafts found for re-eval")
		return
	}
	payload := buildPromptEvaluationSkillReEvalPayload(sourceAsset, candidate, *sourceSnapshot, reEvalSnapshot, cases)
	profile := promptEvaluationAssetProfileFromPayload(mustJSONBytes(payload), candidate.PromptID, promptEvaluationAssetTestSuite)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Skill re-eval " + shortPromptEvaluationID(uuidToString(candidate.ID)) + " " + time.Now().UTC().Format("20060102-150405")
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = "Skill candidate re-eval suite generated from approved git-history case drafts."
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start skill re-eval transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	asset, err := qtx.CreatePromptEvaluationAsset(r.Context(), db.CreatePromptEvaluationAssetParams{
		WorkspaceID:              workspaceUUID,
		PromptID:                 candidate.PromptID,
		Name:                     name,
		Description:              description,
		AssetType:                promptEvaluationAssetTestSuite,
		Payload:                  mustJSONBytes(payload),
		Status:                   "启用",
		CreatedBy:                parseUUID(userID),
		StructureSchema:          profile.StructureSchema,
		StructuredCaseCount:      profile.StructuredCaseCount,
		StructuredVariableCount:  profile.StructuredVariableCount,
		StructuredAssertionCount: profile.StructuredAssertionCount,
		LinkedDatasetCount:       profile.LinkedDatasetCount,
		LinkedPromptCount:        profile.LinkedPromptCount,
		EvaluationDimensionCount: profile.EvaluationDimensionCount,
		ExperimentDimensionCount: profile.ExperimentDimensionCount,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill re-eval asset with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create skill re-eval asset")
		return
	}
	if ok := h.syncPromptEvaluationCasesFromPayload(w, r, qtx, asset, parseUUID(userID)); !ok {
		return
	}
	reEvalPlan := map[string]any{
		"required":             true,
		"asset_id":             uuidToString(asset.ID),
		"source_asset_id":      uuidToString(sourceAsset.ID),
		"candidate_id":         uuidToString(candidate.ID),
		"source_snapshot":      sourceSnapshot,
		"re_eval_snapshot":     reEvalSnapshot,
		"case_count":           len(cases),
		"recommended_endpoint": "/api/prompt-evaluation-optimization-candidates/" + uuidToString(candidate.ID) + "/skill-re-eval-run",
		"asset_run_endpoint":   "/api/prompt-evaluation-assets/" + uuidToString(asset.ID) + "/run",
		"reason":               "run this asset after skill apply; old snapshot eval cannot prove the new skill hash",
	}
	updatedCandidate, err := mergePromptEvaluationOptimizationCandidateMetricsRow(r.Context(), tx, workspaceUUID, candidate.ID, map[string]any{
		"skill_re_eval": reEvalPlan,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist skill re-eval evidence")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit skill re-eval asset")
		return
	}
	writeJSON(w, http.StatusCreated, PromptEvaluationSkillReEvalAssetResponse{
		Candidate:      promptEvaluationOptimizationCandidateToResponse(updatedCandidate),
		Asset:          promptEvaluationAssetToResponse(asset),
		SourceSnapshot: *sourceSnapshot,
		ReEvalSnapshot: reEvalSnapshot,
		CaseCount:      len(cases),
		Cases:          cases,
		ReEvalPlan:     reEvalPlan,
	})
}

func (h *Handler) RunPromptEvaluationSkillReEval(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	candidateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation optimization candidate id")
	if !ok {
		return
	}
	candidate, err := h.Queries.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation optimization candidate not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation optimization candidate")
		return
	}
	var req RunPromptEvaluationSkillReEvalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	assetIDText := strings.TrimSpace(req.AssetID)
	if assetIDText == "" {
		assetIDText = skillReEvalAssetIDFromCandidate(candidate)
	}
	if assetIDText == "" {
		writeError(w, http.StatusBadRequest, "skill re-eval asset_id is required")
		return
	}
	assetID, ok := parseUUIDOrBadRequest(w, assetIDText, "skill re-eval asset id")
	if !ok {
		return
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{
		ID:          assetID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "skill re-eval asset not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load skill re-eval asset")
		return
	}
	payload := decodePayloadObject(asset.Payload)
	if err := validatePromptEvaluationSkillReEvalAsset(candidate, asset, payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !asset.PromptID.Valid {
		writeError(w, http.StatusBadRequest, "prompt_id is required to run a skill re-eval asset")
		return
	}
	prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{
		ID:          asset.PromptID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
		return
	}
	cases, ok := h.promptEvaluationCasesForAsset(w, r, asset)
	if !ok {
		return
	}
	result := buildPromptEvaluationRunResult(asset, prompt, payload, cases)
	run, ok := h.persistPromptEvaluationLocalRun(w, r, asset, result, parseUUID(userID))
	if !ok {
		return
	}
	reEvalRun := buildPromptEvaluationSkillReEvalRunEvidence(candidate, asset, run, result, len(cases))
	payload["最近运行"] = result
	payload["运行记录"] = appendPromptEvaluationRunHistory(payload["运行记录"], result)
	payload["skill_re_eval_run"] = reEvalRun
	updatedAsset, err := h.Queries.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
		PromptID:    asset.PromptID,
		Payload:     mustJSONBytes(payload),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save skill re-eval run")
		return
	}
	updatedCandidate, err := h.mergePromptEvaluationOptimizationCandidateMetrics(r.Context(), workspaceUUID, candidate.ID, map[string]any{
		"skill_re_eval_run": reEvalRun,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist skill re-eval run evidence")
		return
	}
	sourceSnapshot, reEvalSnapshot := skillSnapshotsFromReEvalPayload(payload)
	writeJSON(w, http.StatusOK, PromptEvaluationSkillReEvalRunResponse{
		Candidate:      promptEvaluationOptimizationCandidateToResponse(updatedCandidate),
		Asset:          promptEvaluationAssetToResponse(updatedAsset),
		Run:            promptEvaluationRunToResponse(run),
		SourceSnapshot: sourceSnapshot,
		ReEvalSnapshot: reEvalSnapshot,
		CaseCount:      len(cases),
		ProofScope:     "local_prompt_evaluation_run",
		ReEvalRun:      reEvalRun,
	})
}

func (h *Handler) mergePromptEvaluationOptimizationCandidateMetrics(ctx context.Context, workspaceID pgtype.UUID, candidateID pgtype.UUID, patch map[string]any) (db.PromptEvaluationOptimizationCandidate, error) {
	return mergePromptEvaluationOptimizationCandidateMetricsRow(ctx, h.DB, workspaceID, candidateID, patch)
}

func (h *Handler) applyPromptEvaluationSkillSourceResourceDefaults(
	w http.ResponseWriter,
	r *http.Request,
	provider *string,
	repo *string,
	repoPath *string,
	branch *string,
	sourceResourceID *string,
) bool {
	id := strings.TrimSpace(*sourceResourceID)
	if id == "" {
		return true
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return false
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, id, "source_resource_id")
	if !ok {
		return false
	}
	resource, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{
		ID:          resourceUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "source project resource not found")
			return false
		}
		writeError(w, http.StatusInternalServerError, "failed to load source project resource")
		return false
	}
	if err := applyPromptEvaluationSkillProjectResourceDefaults(resource, provider, repo, repoPath, branch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	*sourceResourceID = uuidToString(resource.ID)
	return true
}

func applyPromptEvaluationSkillProjectResourceDefaults(
	resource db.ProjectResource,
	provider *string,
	repo *string,
	repoPath *string,
	branch *string,
) error {
	switch resource.ResourceType {
	case "gongfeng_repo":
		var ref promptEvaluationSkillGongfengResourceRef
		if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil {
			return fmt.Errorf("invalid gongfeng source resource payload: %w", err)
		}
		ref.ProjectPath = strings.Trim(strings.TrimSpace(ref.ProjectPath), "/")
		if ref.ProjectPath == "" {
			return errors.New("gongfeng source resource is missing project_path")
		}
		if strings.TrimSpace(*provider) == "" {
			*provider = "gongfeng"
		}
		if strings.TrimSpace(*repo) == "" {
			*repo = ref.ProjectPath
		}
		if strings.TrimSpace(*branch) == "" && strings.TrimSpace(ref.Ref) != "" {
			*branch = strings.TrimSpace(ref.Ref)
		}
		if strings.TrimSpace(*repoPath) == "" {
			return errors.New("source_resource_id references gongfeng_repo; repo_path local checkout is required until Gongfeng profile checkout integration is available")
		}
		return nil
	case "local_directory":
		var ref promptEvaluationSkillLocalDirectoryResourceRef
		if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil {
			return fmt.Errorf("invalid local_directory source resource payload: %w", err)
		}
		ref.LocalPath = strings.TrimSpace(ref.LocalPath)
		if ref.LocalPath == "" {
			return errors.New("local_directory source resource is missing local_path")
		}
		if strings.TrimSpace(*provider) == "" {
			*provider = "local_directory"
		}
		if strings.TrimSpace(*repoPath) == "" {
			*repoPath = ref.LocalPath
		}
		if strings.TrimSpace(*repo) == "" {
			label := strings.TrimSpace(ref.Label)
			if label == "" && resource.Label.Valid {
				label = strings.TrimSpace(resource.Label.String)
			}
			if label != "" {
				*repo = label
			} else {
				*repo = filepath.Base(ref.LocalPath)
			}
		}
		if strings.TrimSpace(*branch) == "" {
			*branch = "HEAD"
		}
		return nil
	default:
		return fmt.Errorf("source_resource_id must reference gongfeng_repo or local_directory, got %s", resource.ResourceType)
	}
}

func mergePromptEvaluationOptimizationCandidateMetricsRow(ctx context.Context, executor dbExecutor, workspaceID pgtype.UUID, candidateID pgtype.UUID, patch map[string]any) (db.PromptEvaluationOptimizationCandidate, error) {
	row := executor.QueryRow(ctx, `
UPDATE prompt_evaluation_optimization_candidate
SET metrics = COALESCE(metrics, '{}'::jsonb) || $3::jsonb,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING id, workspace_id, asset_id, run_id, prompt_id, candidate_name, candidate_content, rationale, failed_case_count, source_failure_summary, source_prompt_snapshot, metrics, status, published_prompt_id, published_at, created_by, created_at, updated_at
`, candidateID, workspaceID, mustJSONBytes(patch))
	var item db.PromptEvaluationOptimizationCandidate
	err := row.Scan(
		&item.ID,
		&item.WorkspaceID,
		&item.AssetID,
		&item.RunID,
		&item.PromptID,
		&item.CandidateName,
		&item.CandidateContent,
		&item.Rationale,
		&item.FailedCaseCount,
		&item.SourceFailureSummary,
		&item.SourcePromptSnapshot,
		&item.Metrics,
		&item.Status,
		&item.PublishedPromptID,
		&item.PublishedAt,
		&item.CreatedBy,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	return item, err
}

func (h *Handler) updatePromptEvaluationAssetPayload(w http.ResponseWriter, r *http.Request, asset db.PromptEvaluationAsset, payload map[string]any) (db.PromptEvaluationAsset, bool) {
	profile := promptEvaluationAssetProfileFromPayload(mustJSONBytes(payload), asset.PromptID, asset.AssetType)
	updated, err := h.Queries.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:                       asset.ID,
		WorkspaceID:              asset.WorkspaceID,
		PromptID:                 asset.PromptID,
		Name:                     pgtype.Text{String: asset.Name, Valid: true},
		Description:              pgtype.Text{String: asset.Description, Valid: true},
		AssetType:                pgtype.Text{String: asset.AssetType, Valid: true},
		Payload:                  mustJSONBytes(payload),
		Status:                   pgtype.Text{String: asset.Status, Valid: true},
		StructureSchema:          pgtype.Text{String: profile.StructureSchema, Valid: true},
		StructuredCaseCount:      pgtype.Int4{Int32: profile.StructuredCaseCount, Valid: true},
		StructuredVariableCount:  pgtype.Int4{Int32: profile.StructuredVariableCount, Valid: true},
		StructuredAssertionCount: pgtype.Int4{Int32: profile.StructuredAssertionCount, Valid: true},
		LinkedDatasetCount:       pgtype.Int4{Int32: profile.LinkedDatasetCount, Valid: true},
		LinkedPromptCount:        pgtype.Int4{Int32: profile.LinkedPromptCount, Valid: true},
		EvaluationDimensionCount: pgtype.Int4{Int32: profile.EvaluationDimensionCount, Valid: true},
		ExperimentDimensionCount: pgtype.Int4{Int32: profile.ExperimentDimensionCount, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update prompt evaluation asset payload")
		return db.PromptEvaluationAsset{}, false
	}
	return updated, true
}
