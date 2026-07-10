package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func buildPromptEvaluationSkillInventory(req CreatePromptEvaluationSkillInventoryRequest, now time.Time) (PromptEvaluationSkillInventoryResult, error) {
	skillRoot := strings.TrimSpace(req.SkillRoot)
	if skillRoot == "" {
		skillRoot = ".codebuddy/skills"
	}
	repoPath, skillRoot, err := validateLocalRepoRelativePath(req.RepoPath, skillRoot, "skill_root")
	if err != nil {
		return PromptEvaluationSkillInventoryResult{}, err
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "HEAD"
	}
	headCommit, err := gitOutput(repoPath, "rev-parse", branch)
	if err != nil {
		return PromptEvaluationSkillInventoryResult{}, fmt.Errorf("failed to resolve branch commit: %w", err)
	}
	if branch == "HEAD" {
		if currentBranch, err := gitOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && currentBranch != "" {
			branch = currentBranch
		}
	}
	rootPath := filepath.Join(repoPath, filepath.FromSlash(skillRoot))
	if info, err := os.Stat(rootPath); err != nil {
		return PromptEvaluationSkillInventoryResult{}, fmt.Errorf("skill_root not found: %w", err)
	} else if !info.IsDir() {
		return PromptEvaluationSkillInventoryResult{}, errors.New("skill_root must be a directory")
	}

	items := []PromptEvaluationSkillInventoryItem{}
	err = filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "SKILL.md" {
			return nil
		}
		relPath, err := filepath.Rel(repoPath, path)
		if err != nil {
			return err
		}
		skillPath := filepath.ToSlash(relPath)
		item, err := buildPromptEvaluationSkillInventoryItem(repoPath, headCommit, skillPath)
		if err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	if err != nil {
		return PromptEvaluationSkillInventoryResult{}, fmt.Errorf("failed to scan skill inventory: %w", err)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].SkillPath < items[j].SkillPath
	})

	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = "gongfeng"
	}
	repo := strings.TrimSpace(req.Repo)
	if repo == "" {
		repo = filepath.Base(repoPath)
	}
	return PromptEvaluationSkillInventoryResult{
		SchemaVersion:    promptEvaluationSkillInventorySchema,
		Provider:         provider,
		Repo:             repo,
		RepoPath:         repoPath,
		Branch:           branch,
		HeadCommit:       headCommit,
		SkillRoot:        skillRoot,
		Items:            items,
		DiscoveredCount:  len(items),
		SnapshotTime:     now.Format(time.RFC3339Nano),
		SourceResourceID: strings.TrimSpace(req.SourceResourceID),
	}, nil
}

func buildPromptEvaluationSkillInventoryItem(repoPath string, headCommit string, skillPath string) (PromptEvaluationSkillInventoryItem, error) {
	content, err := gitBlobContent(repoPath, headCommit, skillPath)
	tracked := true
	if err != nil {
		tracked = false
		content, err = os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(skillPath)))
		if err != nil {
			return PromptEvaluationSkillInventoryItem{}, fmt.Errorf("failed to read skill file %s: %w", skillPath, err)
		}
	}
	item := PromptEvaluationSkillInventoryItem{
		SkillPath:    skillPath,
		SkillName:    skillNameFromContent(content, filepath.Base(filepath.Dir(skillPath))),
		SkillHash:    sha256Hex(content),
		HasChangelog: false,
		Tracked:      tracked,
	}
	changelogPath := filepath.ToSlash(filepath.Join(filepath.Dir(skillPath), "CHANGELOG.md"))
	if _, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(changelogPath))); err == nil {
		item.ChangelogPath = changelogPath
		item.HasChangelog = true
	}
	if raw, err := gitOutput(repoPath, "log", "-1", "--format=%H%x1f%s%x1f%cI", "--", skillPath); err == nil && strings.TrimSpace(raw) != "" {
		parts := strings.SplitN(raw, "\x1f", 3)
		item.LastCommit = parts[0]
		if len(parts) > 1 {
			item.LastCommitSubject = parts[1]
		}
		if len(parts) > 2 {
			item.LastUpdatedAt = parts[2]
		}
	}
	return item, nil
}

func buildPromptEvaluationSkillSnapshot(req CreatePromptEvaluationSkillSnapshotRequest, now time.Time) (PromptEvaluationSkillSnapshotResponse, error) {
	repoPath, skillPath, err := validateLocalRepoSkillPath(req.RepoPath, req.SkillPath)
	if err != nil {
		return PromptEvaluationSkillSnapshotResponse{}, err
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "HEAD"
	}
	baseCommit, err := gitOutput(repoPath, "rev-parse", branch)
	if err != nil {
		return PromptEvaluationSkillSnapshotResponse{}, fmt.Errorf("failed to resolve branch commit: %w", err)
	}
	if branch == "HEAD" {
		if currentBranch, err := gitOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && currentBranch != "" {
			branch = currentBranch
		}
	}
	content, err := gitBlobContent(repoPath, baseCommit, skillPath)
	if err != nil {
		return PromptEvaluationSkillSnapshotResponse{}, fmt.Errorf("failed to read skill file from base commit: %w", err)
	}
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = "gongfeng"
	}
	repo := strings.TrimSpace(req.Repo)
	if repo == "" {
		repo = filepath.Base(repoPath)
	}
	return PromptEvaluationSkillSnapshotResponse{
		SchemaVersion:    promptEvaluationSkillSnapshotSchema,
		Provider:         provider,
		Repo:             repo,
		RepoPath:         repoPath,
		Branch:           branch,
		BaseCommit:       baseCommit,
		SkillPath:        skillPath,
		SkillHash:        sha256Hex(content),
		SnapshotTime:     now.Format(time.RFC3339Nano),
		SourceResourceID: strings.TrimSpace(req.SourceResourceID),
	}, nil
}

func buildPromptEvaluationSkillAppliedWorktreeSnapshot(req CreatePromptEvaluationSkillSnapshotRequest, applyEvidence PromptEvaluationSkillApplyResult, now time.Time) (PromptEvaluationSkillSnapshotResponse, error) {
	repoPath, skillPath, err := validateLocalRepoSkillPath(req.RepoPath, req.SkillPath)
	if err != nil {
		return PromptEvaluationSkillSnapshotResponse{}, err
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "HEAD"
	}
	headCommit, err := gitOutput(repoPath, "rev-parse", branch)
	if err != nil {
		return PromptEvaluationSkillSnapshotResponse{}, fmt.Errorf("failed to resolve branch commit: %w", err)
	}
	content, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(skillPath)))
	if err != nil {
		return PromptEvaluationSkillSnapshotResponse{}, fmt.Errorf("failed to read applied skill file from worktree: %w", err)
	}
	skillHash := sha256Hex(content)
	if applyEvidence.SkillHashAfter != "" && applyEvidence.SkillHashAfter != skillHash {
		return PromptEvaluationSkillSnapshotResponse{}, fmt.Errorf("applied worktree skill hash %s does not match apply evidence %s", skillHash, applyEvidence.SkillHashAfter)
	}
	return PromptEvaluationSkillSnapshotResponse{
		SchemaVersion:    promptEvaluationSkillSnapshotSchema,
		Provider:         firstNonEmpty(req.Provider, applyEvidence.Snapshot.Provider, "local"),
		Repo:             firstNonEmpty(req.Repo, applyEvidence.Snapshot.Repo, filepath.Base(repoPath)),
		RepoPath:         repoPath,
		Branch:           branch,
		BaseCommit:       headCommit,
		SkillPath:        skillPath,
		SkillHash:        skillHash,
		SnapshotTime:     now.Format(time.RFC3339Nano),
		SourceResourceID: firstNonEmpty(req.SourceResourceID, applyEvidence.Snapshot.SourceResourceID),
	}, nil
}

func buildPromptEvaluationSkillCaseDrafts(req CreatePromptEvaluationSkillCaseDraftsRequest) ([]PromptEvaluationSkillCaseDraft, error) {
	repoPath, skillPath, err := validateLocalRepoSkillPath(req.RepoPath, req.SkillPath)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	raw, err := gitOutput(repoPath, "log", fmt.Sprintf("-%d", limit), "--format=%H%x1f%s", "--", skillPath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect skill git history: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	drafts := make([]PromptEvaluationSkillCaseDraft, 0, len(lines))
	status := "draft"
	if req.AutoApprove {
		status = "approved"
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 2)
		commit := parts[0]
		subject := ""
		if len(parts) > 1 {
			subject = parts[1]
		}
		afterHash := gitBlobHash(repoPath, commit, skillPath)
		beforeHash := gitBlobHash(repoPath, commit+"^", skillPath)
		drafts = append(drafts, PromptEvaluationSkillCaseDraft{
			SchemaVersion:       promptEvaluationSkillCaseDraftSchema,
			Status:              status,
			Input:               "Given skill " + skillPath + " at source commit " + commit + ", verify the behavior implied by: " + subject,
			ExpectedBehavior:    "The skill instructions preserve the intent of commit " + commit + " without regressing existing SOP behavior.",
			Verification:        "Review the SKILL.md diff and rerun the project-specific skill or harness checks referenced by the commit.",
			EvidenceSource:      "git:" + commit + ":" + skillPath,
			ApplicableSkillHash: afterHash,
			ApplicableScope:     skillPath,
			SourceCommit:        commit,
			CommitSubject:       subject,
			SkillPath:           skillPath,
			BeforeHash:          beforeHash,
			AfterHash:           afterHash,
		})
	}
	return drafts, nil
}

func checkPromptEvaluationSkillFreshness(req CheckPromptEvaluationSkillFreshnessRequest, snapshot PromptEvaluationSkillSnapshotResponse, now time.Time) (PromptEvaluationSkillFreshnessResult, error) {
	repoPath, skillPath, err := validateLocalRepoSkillPath(firstNonEmpty(req.RepoPath, snapshot.RepoPath), firstNonEmpty(req.SkillPath, snapshot.SkillPath))
	if err != nil {
		return PromptEvaluationSkillFreshnessResult{}, err
	}
	createOperationSkill := req.CandidateIntent == "create_operation_skill"
	targetBranch := firstNonEmpty(req.TargetBranch, snapshot.Branch, "HEAD")
	headCommit, err := gitOutput(repoPath, "rev-parse", targetBranch)
	if err != nil {
		return PromptEvaluationSkillFreshnessResult{}, fmt.Errorf("failed to resolve target branch: %w", err)
	}
	content, err := gitBlobContent(repoPath, headCommit, skillPath)
	if err != nil {
		if !createOperationSkill {
			return PromptEvaluationSkillFreshnessResult{}, fmt.Errorf("failed to read current skill file from target branch: %w", err)
		}
		content = nil
	}
	currentHash := sha256Hex(content)
	result := PromptEvaluationSkillFreshnessResult{
		SchemaVersion:    promptEvaluationSkillFreshnessSchema,
		Status:           "fresh",
		Reason:           "target branch still matches snapshot base commit",
		TargetBranch:     targetBranch,
		HeadCommit:       headCommit,
		BaseCommit:       snapshot.BaseCommit,
		SkillPath:        skillPath,
		BaseSkillHash:    snapshot.SkillHash,
		CurrentSkillHash: currentHash,
		PatchCheck:       "not_needed",
		CheckedAt:        now.Format(time.RFC3339Nano),
		Snapshot:         snapshot,
	}
	if createOperationSkill {
		if len(content) > 0 {
			result.Status = "conflict"
			result.Reason = "operation skill target already exists on target branch"
			result.PatchCheck = "target_exists"
			return result, nil
		}
		if strings.TrimSpace(req.CandidatePatch) == "" {
			result.Status = "stale"
			result.Reason = "create_operation_skill candidate requires a patch that creates the target skill"
			result.PatchCheck = "missing_patch"
			return result, nil
		}
		if err := gitApplyCheck(repoPath, req.CandidatePatch); err != nil {
			result.Status = "conflict"
			result.Reason = "create_operation_skill patch does not apply cleanly"
			result.PatchCheck = "conflict"
			return result, nil
		}
		result.Reason = "operation skill target does not exist and candidate patch creates it cleanly"
		result.PatchCheck = "creates_file"
		return result, nil
	}
	if headCommit == snapshot.BaseCommit {
		return result, nil
	}
	if currentHash == snapshot.SkillHash {
		result.Status = "branch_changed_skill_unchanged"
		result.Reason = "target branch moved, but skill file hash still matches snapshot"
		return result, nil
	}
	patch := req.CandidatePatch
	if strings.TrimSpace(patch) == "" {
		result.Status = "stale"
		result.Reason = "target branch and skill file changed, and no candidate patch was provided"
		result.PatchCheck = "missing_patch"
		return result, nil
	}
	if err := gitApplyCheck(repoPath, patch); err != nil {
		result.Status = "conflict"
		result.Reason = "target skill changed and candidate patch no longer applies cleanly"
		result.PatchCheck = "conflict"
		return result, nil
	}
	result.Status = "rebaseable"
	result.Reason = "target skill changed, but candidate patch can be replayed cleanly"
	result.PatchCheck = "applies"
	return result, nil
}

func applyPromptEvaluationSkillCandidate(req ApplyPromptEvaluationSkillCandidateRequest, snapshot PromptEvaluationSkillSnapshotResponse, reEvalContext map[string]any, now time.Time) (PromptEvaluationSkillApplyResult, error) {
	repoPath, skillPath, err := validateLocalRepoSkillPath(firstNonEmpty(req.RepoPath, snapshot.RepoPath), firstNonEmpty(req.SkillPath, snapshot.SkillPath))
	if err != nil {
		return PromptEvaluationSkillApplyResult{}, err
	}
	req.RepoPath = repoPath
	req.SkillPath = skillPath
	req.TargetBranch = firstNonEmpty(req.TargetBranch, snapshot.Branch, "HEAD")
	if strings.TrimSpace(req.CandidatePatch) == "" {
		return PromptEvaluationSkillApplyResult{}, errors.New("candidate_patch is required")
	}
	freshness, err := checkPromptEvaluationSkillFreshness(CheckPromptEvaluationSkillFreshnessRequest{
		RepoPath:        repoPath,
		TargetBranch:    req.TargetBranch,
		SkillPath:       skillPath,
		CandidatePatch:  req.CandidatePatch,
		CandidateIntent: req.CandidateIntent,
	}, snapshot, now)
	if err != nil {
		return PromptEvaluationSkillApplyResult{}, err
	}
	changelogPath := ""
	if !req.SkipChangelog {
		changelogPath, err = normalizePromptEvaluationChangelogPath(skillPath, req.ChangelogPath)
		if err != nil {
			return PromptEvaluationSkillApplyResult{}, err
		}
	}
	result := PromptEvaluationSkillApplyResult{
		SchemaVersion:   promptEvaluationSkillApplySchema,
		Status:          "blocked",
		RepoPath:        repoPath,
		TargetBranch:    req.TargetBranch,
		HeadCommit:      freshness.HeadCommit,
		SkillPath:       skillPath,
		SkillHashBefore: freshness.CurrentSkillHash,
		ChangelogPath:   changelogPath,
		PatchCheck:      "not_run",
		Freshness:       freshness,
		ReEvalRequired:  true,
		ReEvalPlan:      buildPromptEvaluationSkillReEvalPlan(snapshot, freshness, reEvalContext),
		CheckedAt:       now.Format(time.RFC3339Nano),
		Snapshot:        snapshot,
	}
	if freshness.Status == "stale" || freshness.Status == "conflict" {
		result.Reason = "freshness check blocked apply: " + freshness.Status
		result.PatchCheck = freshness.PatchCheck
		return result, nil
	}
	if err := gitApplyCheck(repoPath, req.CandidatePatch); err != nil {
		result.Status = "conflict"
		result.Reason = "candidate patch does not apply cleanly: " + err.Error()
		result.PatchCheck = "conflict"
		return result, nil
	}
	result.PatchCheck = "applies"
	dirtyFiles, err := gitStatusShort(repoPath)
	if err != nil {
		return PromptEvaluationSkillApplyResult{}, err
	}
	if len(dirtyFiles) > 0 && !req.AllowDirty {
		result.Status = "blocked"
		result.Reason = "worktree has uncommitted changes; set allow_dirty only for controlled development fixtures"
		result.ChangedFiles = dirtyFiles
		return result, nil
	}
	if req.DryRun {
		result.Status = "dry_run"
		result.Reason = "candidate patch and changelog plan verified; no files were modified"
		return result, nil
	}
	if err := gitApplyPatch(repoPath, req.CandidatePatch); err != nil {
		result.Status = "conflict"
		result.Reason = "candidate patch failed during apply: " + err.Error()
		result.PatchCheck = "conflict"
		return result, nil
	}
	if !req.SkipChangelog {
		if err := appendPromptEvaluationSkillChangelog(repoPath, changelogPath, snapshot, freshness, req, now); err != nil {
			return PromptEvaluationSkillApplyResult{}, err
		}
	}
	afterContent, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(skillPath)))
	if err != nil {
		return PromptEvaluationSkillApplyResult{}, fmt.Errorf("failed to read applied skill file: %w", err)
	}
	changedFiles, err := gitStatusShort(repoPath)
	if err != nil {
		return PromptEvaluationSkillApplyResult{}, err
	}
	result.Status = "applied"
	result.Reason = "candidate patch applied to local worktree; re-eval is required before publishing evidence"
	result.SkillHashAfter = sha256Hex(afterContent)
	result.ChangedFiles = changedFiles
	result.AppliedAt = now.Format(time.RFC3339Nano)
	result.ReEvalPlan["applied_skill_hash"] = result.SkillHashAfter
	result.ReEvalPlan["changed_files"] = changedFiles
	return result, nil
}

func skillSnapshotFromCandidate(candidate db.PromptEvaluationOptimizationCandidate) *PromptEvaluationSkillSnapshotResponse {
	for _, raw := range [][]byte{candidate.SourceFailureSummary, candidate.Metrics, candidate.SourcePromptSnapshot} {
		payload, ok := decodeJSONDefault(raw, map[string]any{}).(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"skill_snapshot", "Skill Snapshot", "技能快照"} {
			if snapshot, ok := decodeSkillSnapshotAny(payload[key]); ok {
				return &snapshot
			}
		}
	}
	return nil
}

func normalizePromptEvaluationSkillPatch(raw PromptEvaluationSkillPatch, candidate db.PromptEvaluationOptimizationCandidate, now time.Time) (PromptEvaluationSkillPatch, error) {
	if strings.TrimSpace(raw.Patch) == "" {
		return PromptEvaluationSkillPatch{}, errors.New("skill_patch.patch is required")
	}
	sourceSnapshot := raw.SourceSnapshot
	if sourceSnapshot == nil {
		sourceSnapshot = skillSnapshotFromCandidate(candidate)
	}
	if sourceSnapshot == nil {
		return PromptEvaluationSkillPatch{}, errors.New("skill_patch.source_snapshot is required")
	}
	candidateIntent := firstNonEmpty(raw.CandidateIntent, "update_existing_skill")
	if candidateIntent != "update_existing_skill" && candidateIntent != "create_operation_skill" {
		return PromptEvaluationSkillPatch{}, errors.New("skill_patch.candidate_intent must be update_existing_skill or create_operation_skill")
	}
	createdAt := strings.TrimSpace(raw.CreatedAt)
	if createdAt == "" {
		createdAt = now.Format(time.RFC3339Nano)
	}
	patch := raw.Patch
	normalized := PromptEvaluationSkillPatch{
		SchemaVersion:        promptEvaluationSkillPatchSchema,
		Patch:                patch,
		PatchHash:            sha256Hex([]byte(patch)),
		PatchBytes:           len([]byte(patch)),
		CandidateIntent:      candidateIntent,
		OperationSkillKey:    strings.TrimSpace(raw.OperationSkillKey),
		OperationSkillPath:   strings.TrimSpace(raw.OperationSkillPath),
		OperationSkillReason: strings.TrimSpace(raw.OperationSkillReason),
		SourceSnapshot:       sourceSnapshot,
		SourceResourceID:     firstNonEmpty(raw.SourceResourceID, sourceSnapshot.SourceResourceID),
		RepoPath:             firstNonEmpty(raw.RepoPath, sourceSnapshot.RepoPath),
		TargetBranch:         firstNonEmpty(raw.TargetBranch, sourceSnapshot.Branch, "HEAD"),
		SkillPath:            firstNonEmpty(raw.SkillPath, sourceSnapshot.SkillPath),
		ChangelogPath:        strings.TrimSpace(raw.ChangelogPath),
		ExpectedImprovement:  strings.TrimSpace(raw.ExpectedImprovement),
		Risk:                 strings.TrimSpace(raw.Risk),
		VerificationPlan:     strings.TrimSpace(raw.VerificationPlan),
		PublicationStatus:    firstNonEmpty(raw.PublicationStatus, "draft"),
		CreatedAt:            createdAt,
		UpdatedAt:            now.Format(time.RFC3339Nano),
	}
	return normalized, nil
}

func skillPatchFromCandidate(candidate db.PromptEvaluationOptimizationCandidate) *PromptEvaluationSkillPatch {
	metrics := decodePayloadObject(candidate.Metrics)
	raw, ok := metrics["skill_patch"]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var patch PromptEvaluationSkillPatch
	if err := json.Unmarshal(encoded, &patch); err != nil {
		return nil
	}
	if patch.SchemaVersion == "" || strings.TrimSpace(patch.Patch) == "" {
		return nil
	}
	if patch.PatchHash == "" {
		patch.PatchHash = sha256Hex([]byte(patch.Patch))
	}
	if patch.PatchBytes == 0 {
		patch.PatchBytes = len([]byte(patch.Patch))
	}
	if strings.TrimSpace(patch.CandidateIntent) == "" {
		patch.CandidateIntent = "update_existing_skill"
	}
	return &patch
}

func applySkillPatchFreshnessDefaults(req *CheckPromptEvaluationSkillFreshnessRequest, patch *PromptEvaluationSkillPatch) {
	if patch == nil {
		return
	}
	if req.CandidateIntent == "" {
		req.CandidateIntent = patch.CandidateIntent
	}
	if req.CandidatePatch == "" {
		req.CandidatePatch = patch.Patch
	}
	if req.SourceResourceID == "" {
		req.SourceResourceID = patch.SourceResourceID
	}
	if req.RepoPath == "" {
		req.RepoPath = patch.RepoPath
	}
	if req.TargetBranch == "" {
		req.TargetBranch = patch.TargetBranch
	}
	if req.SkillPath == "" {
		req.SkillPath = patch.SkillPath
	}
}

func applySkillPatchApplyDefaults(req *ApplyPromptEvaluationSkillCandidateRequest, patch *PromptEvaluationSkillPatch) {
	if patch == nil {
		return
	}
	if req.CandidateIntent == "" {
		req.CandidateIntent = patch.CandidateIntent
	}
	if req.CandidatePatch == "" {
		req.CandidatePatch = patch.Patch
	}
	if req.SourceResourceID == "" {
		req.SourceResourceID = patch.SourceResourceID
	}
	if req.RepoPath == "" {
		req.RepoPath = patch.RepoPath
	}
	if req.TargetBranch == "" {
		req.TargetBranch = patch.TargetBranch
	}
	if req.SkillPath == "" {
		req.SkillPath = patch.SkillPath
	}
	if req.ChangelogPath == "" {
		req.ChangelogPath = patch.ChangelogPath
	}
	if req.ChangeReason == "" {
		req.ChangeReason = patch.ExpectedImprovement
	}
	if req.VerificationResult == "" {
		req.VerificationResult = patch.VerificationPlan
	}
}

func applySkillPatchReEvalDefaults(req *PreparePromptEvaluationSkillReEvalRequest, patch *PromptEvaluationSkillPatch) {
	if patch == nil {
		return
	}
	if req.SourceResourceID == "" {
		req.SourceResourceID = patch.SourceResourceID
	}
	if req.RepoPath == "" {
		req.RepoPath = patch.RepoPath
	}
	if req.TargetBranch == "" {
		req.TargetBranch = patch.TargetBranch
	}
	if req.SkillPath == "" {
		req.SkillPath = patch.SkillPath
	}
}

func skillApplyFromCandidate(candidate db.PromptEvaluationOptimizationCandidate) *PromptEvaluationSkillApplyResult {
	metrics := decodePayloadObject(candidate.Metrics)
	raw, ok := metrics["skill_apply"]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var apply PromptEvaluationSkillApplyResult
	if err := json.Unmarshal(encoded, &apply); err != nil {
		return nil
	}
	if apply.Status == "" || apply.SkillPath == "" || apply.SkillHashAfter == "" {
		return nil
	}
	return &apply
}

func skillSnapshotFromAsset(asset db.PromptEvaluationAsset) *PromptEvaluationSkillSnapshotResponse {
	payload := decodePayloadObject(asset.Payload)
	for _, key := range []string{"skill_snapshot", "re_eval_snapshot", "source_skill_snapshot"} {
		if snapshot, ok := decodeSkillSnapshotAny(payload[key]); ok {
			return &snapshot
		}
	}
	return nil
}

func decodeSkillSnapshotAny(value any) (PromptEvaluationSkillSnapshotResponse, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return PromptEvaluationSkillSnapshotResponse{}, false
	}
	var snapshot PromptEvaluationSkillSnapshotResponse
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return PromptEvaluationSkillSnapshotResponse{}, false
	}
	if snapshot.BaseCommit == "" || snapshot.SkillPath == "" || snapshot.SkillHash == "" {
		return PromptEvaluationSkillSnapshotResponse{}, false
	}
	return snapshot, true
}

func skillCaseDraftsFromAsset(asset db.PromptEvaluationAsset) []PromptEvaluationSkillCaseDraft {
	payload := decodePayloadObject(asset.Payload)
	rawList, ok := payload["skill_case_drafts"].([]any)
	if !ok {
		return nil
	}
	drafts := make([]PromptEvaluationSkillCaseDraft, 0, len(rawList))
	for _, rawItem := range rawList {
		encoded, err := json.Marshal(rawItem)
		if err != nil {
			continue
		}
		var draft PromptEvaluationSkillCaseDraft
		if err := json.Unmarshal(encoded, &draft); err != nil {
			continue
		}
		if draft.SourceCommit == "" || draft.SkillPath == "" {
			continue
		}
		drafts = append(drafts, draft)
	}
	return drafts
}

func skillReEvalAssetIDFromCandidate(candidate db.PromptEvaluationOptimizationCandidate) string {
	metrics := decodePayloadObject(candidate.Metrics)
	reEvalPlan := asMap(metrics["skill_re_eval"])
	return stringFromAny(reEvalPlan["asset_id"])
}

func validatePromptEvaluationSkillReEvalAsset(candidate db.PromptEvaluationOptimizationCandidate, asset db.PromptEvaluationAsset, payload map[string]any) error {
	if asset.AssetType != promptEvaluationAssetTestSuite {
		return errors.New("skill re-eval asset must be a test suite")
	}
	if stringFromAny(payload["skill_re_eval_contract"]) != "multica.skill.re_eval.v1" {
		return errors.New("asset is not a skill re-eval asset")
	}
	if sourceCandidateID := stringFromAny(payload["source_candidate_id"]); sourceCandidateID != "" && sourceCandidateID != uuidToString(candidate.ID) {
		return errors.New("skill re-eval asset does not belong to this candidate")
	}
	if _, ok := decodeSkillSnapshotAny(payload["re_eval_snapshot"]); !ok {
		return errors.New("skill re-eval asset is missing re_eval_snapshot")
	}
	return nil
}

func skillSnapshotsFromReEvalPayload(payload map[string]any) (PromptEvaluationSkillSnapshotResponse, PromptEvaluationSkillSnapshotResponse) {
	sourceSnapshot, _ := decodeSkillSnapshotAny(payload["source_skill_snapshot"])
	reEvalSnapshot, _ := decodeSkillSnapshotAny(payload["re_eval_snapshot"])
	return sourceSnapshot, reEvalSnapshot
}

func buildPromptEvaluationSkillReEvalRunEvidence(candidate db.PromptEvaluationOptimizationCandidate, asset db.PromptEvaluationAsset, run db.PromptEvaluationRun, result promptEvaluationRunResult, caseCount int) map[string]any {
	return map[string]any{
		"candidate_id":   uuidToString(candidate.ID),
		"asset_id":       uuidToString(asset.ID),
		"run_id":         uuidToString(run.ID),
		"status":         run.Status,
		"run_kind":       promptEvaluationRunKindLabel(run.RunKind),
		"case_count":     caseCount,
		"passed_cases":   result.PassedCases,
		"failed_cases":   result.FailedCases,
		"pass_rate":      result.PassRate,
		"proof_scope":    "local_prompt_evaluation_run",
		"proof_boundary": "executes the prepared re-eval asset through the existing prompt evaluation runner; Gongfeng/agent skill runtime execution still requires separate evidence",
		"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func buildPromptEvaluationSkillReEvalCases(drafts []PromptEvaluationSkillCaseDraft, snapshot PromptEvaluationSkillSnapshotResponse, req PreparePromptEvaluationSkillReEvalRequest) []PromptEvaluationSkillReEvalCase {
	allowed := map[string]bool{}
	statuses := req.Statuses
	if len(statuses) == 0 {
		statuses = []string{"approved", "active"}
		if req.IncludeDraft {
			statuses = append(statuses, "draft")
		}
	}
	for _, status := range statuses {
		status = strings.ToLower(strings.TrimSpace(status))
		if status != "" {
			allowed[status] = true
		}
	}
	cases := make([]PromptEvaluationSkillReEvalCase, 0, len(drafts))
	for _, draft := range drafts {
		status := strings.ToLower(strings.TrimSpace(draft.Status))
		if status == "" {
			status = "draft"
		}
		if !allowed[status] {
			continue
		}
		name := strings.TrimSpace(draft.CommitSubject)
		if name == "" {
			name = "Skill case " + shortPromptEvaluationID(draft.SourceCommit)
		}
		expectedContains := compactStrings([]string{draft.ExpectedBehavior, draft.Verification, draft.EvidenceSource})
		variables := map[string]any{
			"skill_path":            draft.SkillPath,
			"source_commit":         draft.SourceCommit,
			"commit_subject":        draft.CommitSubject,
			"input":                 draft.Input,
			"expected_behavior":     draft.ExpectedBehavior,
			"verification":          draft.Verification,
			"evidence_source":       draft.EvidenceSource,
			"applicable_skill_hash": draft.ApplicableSkillHash,
			"re_eval_skill_hash":    snapshot.SkillHash,
			"re_eval_base_commit":   snapshot.BaseCommit,
		}
		cases = append(cases, PromptEvaluationSkillReEvalCase{
			Name:             name,
			Variables:        variables,
			ExpectedContains: expectedContains,
			Tags:             compactStrings([]string{"skill", "re-eval", status, draft.SkillPath}),
			Input: map[string]any{
				"skill_path":      draft.SkillPath,
				"source_commit":   draft.SourceCommit,
				"evidence_source": draft.EvidenceSource,
				"case_input":      draft.Input,
			},
			Expected: map[string]any{
				"expected_behavior": draft.ExpectedBehavior,
				"verification":      draft.Verification,
				"snapshot":          snapshot,
			},
			SourceCommit:   draft.SourceCommit,
			EvidenceSource: draft.EvidenceSource,
			Status:         status,
		})
	}
	return cases
}

func buildPromptEvaluationSkillReEvalPayload(sourceAsset db.PromptEvaluationAsset, candidate db.PromptEvaluationOptimizationCandidate, sourceSnapshot PromptEvaluationSkillSnapshotResponse, reEvalSnapshot PromptEvaluationSkillSnapshotResponse, cases []PromptEvaluationSkillReEvalCase) map[string]any {
	payloadCases := make([]map[string]any, 0, len(cases))
	for _, item := range cases {
		payloadCases = append(payloadCases, map[string]any{
			"name":              item.Name,
			"case_name":         item.Name,
			"variables":         item.Variables,
			"expected_contains": item.ExpectedContains,
			"input":             item.Input,
			"expected":          item.Expected,
			"tags":              item.Tags,
			"source_commit":     item.SourceCommit,
			"evidence_source":   item.EvidenceSource,
			"status":            item.Status,
		})
	}
	return normalizePromptEvaluationPayloadObject(map[string]any{
		"schema_version":         1,
		"schema":                 "multica.skill.re_eval.payload.v1",
		"语义版本":                   "multica.skill.re_eval.v1",
		"optimization_target":    "skill",
		"skill_re_eval_contract": "multica.skill.re_eval.v1",
		"source_asset_id":        uuidToString(sourceAsset.ID),
		"source_candidate_id":    uuidToString(candidate.ID),
		"source_run_id":          uuidToString(candidate.RunID),
		"source_skill_snapshot":  sourceSnapshot,
		"skill_snapshot":         reEvalSnapshot,
		"re_eval_snapshot":       reEvalSnapshot,
		"re_eval_required":       true,
		"re_eval_reason":         "Old Eval evidence only proves the source snapshot; this asset evaluates the post-apply skill snapshot.",
		"cases":                  payloadCases,
		"metric_contract":        []string{"case_count", "pass_rate", "failed_cases", "skill_snapshot", "source_commit"},
		"指标口径":                   []string{"按 skill 历史 case draft 转换为可运行评测用例", "运行结果只证明 re_eval_snapshot，不反推 source_snapshot"},
	})
}

func shortPromptEvaluationID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func validateLocalRepoSkillPath(repoPath string, skillPath string) (string, string, error) {
	return validateLocalRepoRelativePath(repoPath, skillPath, "skill_path")
}

func validateLocalRepoRelativePath(repoPath string, relativePath string, fieldName string) (string, string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", "", errors.New("repo_path is required")
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid repo_path: %w", err)
	}
	if _, err := os.Stat(filepath.Join(absRepo, ".git")); err != nil {
		return "", "", errors.New("repo_path must be a local git worktree")
	}
	cleanRelative := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relativePath)))
	if cleanRelative == "." || cleanRelative == "" || strings.HasPrefix(cleanRelative, "../") || filepath.IsAbs(cleanRelative) {
		return "", "", fmt.Errorf("%s must be a relative path inside the repo", fieldName)
	}
	return absRepo, cleanRelative, nil
}

func skillNameFromContent(content []byte, fallback string) string {
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			if name != "" {
				return name
			}
		}
	}
	return fallback
}

func gitOutput(repoPath string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitApplyCheck(repoPath string, patch string) error {
	cmd := exec.Command("git", "-C", repoPath, "apply", "--check", "-")
	cmd.Stdin = strings.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func gitApplyPatch(repoPath string, patch string) error {
	cmd := exec.Command("git", "-C", repoPath, "apply", "-")
	cmd.Stdin = strings.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func gitStatusShort(repoPath string) ([]string, error) {
	raw, err := gitOutput(repoPath, "status", "--short")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	items := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items, nil
}

func gitBlobHash(repoPath string, ref string, skillPath string) string {
	out, err := gitBlobContent(repoPath, ref, skillPath)
	if err != nil {
		return ""
	}
	return sha256Hex(out)
}

func gitBlobContent(repoPath string, ref string, skillPath string) ([]byte, error) {
	cmd := exec.Command("git", "-C", repoPath, "show", ref+":"+skillPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func skillCaseDraftsAsAny(drafts []PromptEvaluationSkillCaseDraft) []any {
	values := make([]any, 0, len(drafts))
	for _, draft := range drafts {
		values = append(values, draft)
	}
	return values
}

func normalizePromptEvaluationChangelogPath(skillPath string, requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return filepath.ToSlash(filepath.Join(filepath.Dir(skillPath), "CHANGELOG.md")), nil
	}
	cleanPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(requested)))
	if cleanPath == "." || cleanPath == "" || strings.HasPrefix(cleanPath, "../") || filepath.IsAbs(cleanPath) {
		return "", errors.New("changelog_path must be a relative path inside the repo")
	}
	return cleanPath, nil
}

func appendPromptEvaluationSkillChangelog(repoPath string, changelogPath string, snapshot PromptEvaluationSkillSnapshotResponse, freshness PromptEvaluationSkillFreshnessResult, req ApplyPromptEvaluationSkillCandidateRequest, now time.Time) error {
	fullPath := filepath.Join(repoPath, filepath.FromSlash(changelogPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("failed to create changelog directory: %w", err)
	}
	existing, err := os.ReadFile(fullPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read changelog: %w", err)
	}
	changeReason := firstNonEmpty(strings.TrimSpace(req.ChangeReason), "Skill optimization candidate applied from Eval/Trace evidence.")
	verification := firstNonEmpty(strings.TrimSpace(req.VerificationResult), "Re-eval required before this change can be treated as validated.")
	rollback := firstNonEmpty(strings.TrimSpace(req.RollbackPlan), "Revert the applied patch and rerun the previous snapshot eval.")
	entry := "\n## " + now.Format(time.RFC3339) + " - Skill optimization candidate\n\n" +
		"- Source snapshot: " + snapshot.Repo + " " + snapshot.Branch + "@" + snapshot.BaseCommit + "\n" +
		"- Skill path: " + snapshot.SkillPath + "\n" +
		"- Snapshot skill hash: " + snapshot.SkillHash + "\n" +
		"- Target head: " + freshness.HeadCommit + "\n" +
		"- Freshness result: " + freshness.Status + " (" + freshness.PatchCheck + ")\n" +
		"- Change reason: " + changeReason + "\n" +
		"- Impact scope: " + snapshot.SkillPath + "\n" +
		"- Verification result: " + verification + "\n" +
		"- Rollback: " + rollback + "\n"
	if len(existing) == 0 {
		existing = []byte("# Skill CHANGELOG\n")
	}
	if !bytes.HasSuffix(existing, []byte("\n")) {
		existing = append(existing, '\n')
	}
	existing = append(existing, []byte(entry)...)
	if err := os.WriteFile(fullPath, existing, 0o644); err != nil {
		return fmt.Errorf("failed to write changelog: %w", err)
	}
	return nil
}

func buildPromptEvaluationSkillReEvalPlan(snapshot PromptEvaluationSkillSnapshotResponse, freshness PromptEvaluationSkillFreshnessResult, context map[string]any) map[string]any {
	plan := map[string]any{
		"required":           true,
		"reason":             "old Eval evidence only proves the frozen source snapshot; applied or rebased skill changes must be evaluated again",
		"source_snapshot":    snapshot,
		"freshness_status":   freshness.Status,
		"target_branch":      freshness.TargetBranch,
		"target_head":        freshness.HeadCommit,
		"recommended_action": "run a new PromptEvaluationRun against the active/draft cases bound to the updated skill snapshot",
	}
	for key, value := range context {
		plan[key] = value
	}
	return plan
}

func appendJSONList(existing any, values ...any) []any {
	items := []any{}
	if raw, ok := existing.([]any); ok {
		items = append(items, raw...)
	}
	for _, value := range values {
		items = append(items, value)
	}
	return items
}
