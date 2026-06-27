package handler

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptEvaluationSkillSnapshotCaseDraftsAndFreshness(t *testing.T) {
	repoPath := t.TempDir()
	runSkillTestGit(t, repoPath, "init")
	runSkillTestGit(t, repoPath, "config", "user.email", "test@example.com")
	runSkillTestGit(t, repoPath, "config", "user.name", "Test User")

	skillPath := ".codebuddy/skills/05-verify/SKILL.md"
	v1 := "# Verify\n\n- Run focused checks.\n"
	writeSkillTestFile(t, repoPath, skillPath, v1)
	runSkillTestGit(t, repoPath, "add", skillPath)
	runSkillTestGit(t, repoPath, "commit", "-m", "add verify skill")
	baseCommit := runSkillTestGit(t, repoPath, "rev-parse", "HEAD")

	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	snapshot, err := buildPromptEvaluationSkillSnapshot(CreatePromptEvaluationSkillSnapshotRequest{
		Provider:  "gongfeng",
		Repo:      "example/goal-test",
		RepoPath:  repoPath,
		Branch:    "HEAD",
		SkillPath: skillPath,
	}, now)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if snapshot.BaseCommit != baseCommit {
		t.Fatalf("snapshot base commit = %q, want %q", snapshot.BaseCommit, baseCommit)
	}
	if snapshot.SkillHash != sha256Hex([]byte(v1)) {
		t.Fatalf("snapshot skill hash = %q, want hash of v1", snapshot.SkillHash)
	}
	if snapshot.SchemaVersion != promptEvaluationSkillSnapshotSchema {
		t.Fatalf("snapshot schema = %q", snapshot.SchemaVersion)
	}

	writeSkillTestFile(t, repoPath, "README.md", "non-skill change\n")
	runSkillTestGit(t, repoPath, "add", "README.md")
	runSkillTestGit(t, repoPath, "commit", "-m", "update readme")
	moved, err := checkPromptEvaluationSkillFreshness(CheckPromptEvaluationSkillFreshnessRequest{
		RepoPath: repoPath,
	}, snapshot, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("check moved branch freshness: %v", err)
	}
	if moved.Status != "branch_changed_skill_unchanged" || moved.CurrentSkillHash != snapshot.SkillHash {
		t.Fatalf("moved branch result = %+v", moved)
	}

	v2 := "# Verify\n\n- Run focused checks.\n- Record acceptance evidence.\n"
	writeSkillTestFile(t, repoPath, skillPath, v2)
	runSkillTestGit(t, repoPath, "add", skillPath)
	runSkillTestGit(t, repoPath, "commit", "-m", "require acceptance evidence")
	stale, err := checkPromptEvaluationSkillFreshness(CheckPromptEvaluationSkillFreshnessRequest{
		RepoPath: repoPath,
	}, snapshot, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("check stale freshness: %v", err)
	}
	if stale.Status != "stale" || stale.PatchCheck != "missing_patch" {
		t.Fatalf("stale result = %+v", stale)
	}

	v3 := "# Verify\n\n- Run focused checks.\n- Record acceptance evidence.\n- Attach ledger references.\n"
	writeSkillTestFile(t, repoPath, skillPath, v3)
	patch := runSkillTestGit(t, repoPath, "diff", "--", skillPath)
	writeSkillTestFile(t, repoPath, skillPath, v2)
	rebaseable, err := checkPromptEvaluationSkillFreshness(CheckPromptEvaluationSkillFreshnessRequest{
		RepoPath:       repoPath,
		CandidatePatch: patch,
	}, snapshot, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("check rebaseable freshness: %v", err)
	}
	if rebaseable.Status != "rebaseable" || rebaseable.PatchCheck != "applies" {
		t.Fatalf("rebaseable result = %+v", rebaseable)
	}

	drafts, err := buildPromptEvaluationSkillCaseDrafts(CreatePromptEvaluationSkillCaseDraftsRequest{
		RepoPath:  repoPath,
		SkillPath: skillPath,
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("build case drafts: %v", err)
	}
	if len(drafts) != 2 {
		t.Fatalf("draft count = %d, want 2", len(drafts))
	}
	latest := drafts[0]
	if latest.SchemaVersion != promptEvaluationSkillCaseDraftSchema {
		t.Fatalf("latest schema = %q", latest.SchemaVersion)
	}
	if latest.AfterHash != sha256Hex([]byte(v2)) || latest.BeforeHash != sha256Hex([]byte(v1)) {
		t.Fatalf("latest hashes = before %q after %q", latest.BeforeHash, latest.AfterHash)
	}
	if latest.SourceCommit == "" || latest.EvidenceSource != "git:"+latest.SourceCommit+":"+skillPath {
		t.Fatalf("latest evidence source = %+v", latest)
	}
	if !strings.Contains(latest.Input, "require acceptance evidence") {
		t.Fatalf("latest input does not include commit subject: %q", latest.Input)
	}
}

func TestPromptEvaluationSkillInventoryDiscoversTrackedSkills(t *testing.T) {
	repoPath := t.TempDir()
	runSkillTestGit(t, repoPath, "init")
	runSkillTestGit(t, repoPath, "config", "user.email", "test@example.com")
	runSkillTestGit(t, repoPath, "config", "user.name", "Test User")

	skillPath := ".codebuddy/skills/05-verify/SKILL.md"
	changelogPath := ".codebuddy/skills/05-verify/CHANGELOG.md"
	skill := "# Verify\n\n- Run focused checks.\n"
	writeSkillTestFile(t, repoPath, skillPath, skill)
	writeSkillTestFile(t, repoPath, changelogPath, "# Skill CHANGELOG\n")
	runSkillTestGit(t, repoPath, "add", skillPath, changelogPath)
	runSkillTestGit(t, repoPath, "commit", "-m", "add verify skill")
	headCommit := runSkillTestGit(t, repoPath, "rev-parse", "HEAD")

	now := time.Date(2026, 6, 25, 11, 30, 0, 0, time.UTC)
	inventory, err := buildPromptEvaluationSkillInventory(CreatePromptEvaluationSkillInventoryRequest{
		Provider:  "gongfeng",
		Repo:      "example/goal-test",
		RepoPath:  repoPath,
		Branch:    "HEAD",
		SkillRoot: ".codebuddy/skills",
	}, now)
	if err != nil {
		t.Fatalf("build inventory: %v", err)
	}
	if inventory.SchemaVersion != promptEvaluationSkillInventorySchema {
		t.Fatalf("inventory schema = %q", inventory.SchemaVersion)
	}
	if inventory.HeadCommit != headCommit || inventory.DiscoveredCount != 1 {
		t.Fatalf("inventory head/count = %+v", inventory)
	}
	item := inventory.Items[0]
	if item.SkillPath != skillPath || item.SkillName != "Verify" {
		t.Fatalf("inventory item identity = %+v", item)
	}
	if item.SkillHash != sha256Hex([]byte(skill)) || !item.Tracked {
		t.Fatalf("inventory item hash/tracked = %+v", item)
	}
	if item.LastCommit != headCommit || item.LastCommitSubject != "add verify skill" || item.LastUpdatedAt == "" {
		t.Fatalf("inventory item history = %+v", item)
	}
	if !item.HasChangelog || item.ChangelogPath != changelogPath {
		t.Fatalf("inventory item changelog = %+v", item)
	}
}

func TestPromptEvaluationSkillInventoryRejectsTraversalRoot(t *testing.T) {
	repoPath := t.TempDir()
	runSkillTestGit(t, repoPath, "init")

	_, err := buildPromptEvaluationSkillInventory(CreatePromptEvaluationSkillInventoryRequest{
		RepoPath:  repoPath,
		SkillRoot: "../skills",
	}, time.Date(2026, 6, 25, 11, 45, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "skill_root must be a relative path inside the repo") {
		t.Fatalf("inventory traversal error = %v", err)
	}
}

func TestPromptEvaluationSkillSourceResourceDefaultsFromGongfengRepo(t *testing.T) {
	resource := db.ProjectResource{
		ResourceType: "gongfeng_repo",
		ResourceRef: mustSkillTestJSON(t, map[string]any{
			"provider":      "gongfeng",
			"url":           "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev_sop",
			"project_path":  "ChainWeaver/ida/user-center",
			"resource_kind": "commits",
			"ref":           "v5.0.0_dev_sop",
		}),
	}
	provider, repo, repoPath, branch := "", "", "/data/ida/user-center", ""
	if err := applyPromptEvaluationSkillProjectResourceDefaults(resource, &provider, &repo, &repoPath, &branch); err != nil {
		t.Fatalf("apply gongfeng defaults: %v", err)
	}
	if provider != "gongfeng" || repo != "ChainWeaver/ida/user-center" || branch != "v5.0.0_dev_sop" || repoPath != "/data/ida/user-center" {
		t.Fatalf("gongfeng defaults = provider=%q repo=%q repoPath=%q branch=%q", provider, repo, repoPath, branch)
	}

	provider, repo, repoPath, branch = "", "", "", ""
	err := applyPromptEvaluationSkillProjectResourceDefaults(resource, &provider, &repo, &repoPath, &branch)
	if err == nil || !strings.Contains(err.Error(), "repo_path local checkout is required") {
		t.Fatalf("gongfeng missing checkout error = %v", err)
	}
}

func TestPromptEvaluationSkillSourceResourceDefaultsFromLocalDirectory(t *testing.T) {
	resource := db.ProjectResource{
		ResourceType: "local_directory",
		ResourceRef: mustSkillTestJSON(t, map[string]any{
			"local_path": "/data/ida/goal-test",
			"daemon_id":  "local-dev",
		}),
		Label: pgtype.Text{String: "goal-test checkout", Valid: true},
	}
	provider, repo, repoPath, branch := "", "", "", ""
	if err := applyPromptEvaluationSkillProjectResourceDefaults(resource, &provider, &repo, &repoPath, &branch); err != nil {
		t.Fatalf("apply local_directory defaults: %v", err)
	}
	if provider != "local_directory" || repo != "goal-test checkout" || repoPath != "/data/ida/goal-test" || branch != "HEAD" {
		t.Fatalf("local defaults = provider=%q repo=%q repoPath=%q branch=%q", provider, repo, repoPath, branch)
	}
}

func TestPromptEvaluationSkillApplyWritesChangelogAndRequiresReEval(t *testing.T) {
	repoPath := t.TempDir()
	runSkillTestGit(t, repoPath, "init")
	runSkillTestGit(t, repoPath, "config", "user.email", "test@example.com")
	runSkillTestGit(t, repoPath, "config", "user.name", "Test User")

	skillPath := ".codebuddy/skills/05-verify/SKILL.md"
	v1 := "# Verify\n\n- Run focused checks.\n"
	writeSkillTestFile(t, repoPath, skillPath, v1)
	runSkillTestGit(t, repoPath, "add", skillPath)
	runSkillTestGit(t, repoPath, "commit", "-m", "add verify skill")

	now := time.Date(2026, 6, 25, 13, 0, 0, 0, time.UTC)
	snapshot, err := buildPromptEvaluationSkillSnapshot(CreatePromptEvaluationSkillSnapshotRequest{
		RepoPath:  repoPath,
		Branch:    "HEAD",
		SkillPath: skillPath,
	}, now)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	v2 := "# Verify\n\n- Run focused checks.\n- Attach ledger references.\n"
	writeSkillTestFile(t, repoPath, skillPath, v2)
	patch := runSkillTestGit(t, repoPath, "diff", "--", skillPath)
	writeSkillTestFile(t, repoPath, skillPath, v1)

	result, err := applyPromptEvaluationSkillCandidate(ApplyPromptEvaluationSkillCandidateRequest{
		RepoPath:           repoPath,
		SkillPath:          skillPath,
		CandidatePatch:     patch,
		ChangeReason:       "Improve verification evidence discipline.",
		VerificationResult: "Focused helper test passed.",
		RollbackPlan:       "Reverse the candidate patch.",
	}, snapshot, map[string]any{"candidate_id": "candidate-1"}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("apply skill candidate: %v", err)
	}
	if result.Status != "applied" || result.PatchCheck != "applies" {
		t.Fatalf("apply result = %+v", result)
	}
	if result.SkillHashBefore != sha256Hex([]byte(v1)) || result.SkillHashAfter != sha256Hex([]byte(v2)) {
		t.Fatalf("apply hashes = before %q after %q", result.SkillHashBefore, result.SkillHashAfter)
	}
	if !result.ReEvalRequired || result.ReEvalPlan["candidate_id"] != "candidate-1" {
		t.Fatalf("re-eval plan = %+v", result.ReEvalPlan)
	}
	candidate := db.PromptEvaluationOptimizationCandidate{
		Metrics: mustSkillTestJSON(t, map[string]any{
			"skill_apply": result,
		}),
	}
	applyEvidence := skillApplyFromCandidate(candidate)
	if applyEvidence == nil || applyEvidence.SkillHashAfter != result.SkillHashAfter {
		t.Fatalf("apply evidence decode = %+v", applyEvidence)
	}
	regularSnapshot, err := buildPromptEvaluationSkillSnapshot(CreatePromptEvaluationSkillSnapshotRequest{
		RepoPath:  repoPath,
		Branch:    "HEAD",
		SkillPath: skillPath,
	}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("build regular re-eval snapshot: %v", err)
	}
	if regularSnapshot.SkillHash != snapshot.SkillHash {
		t.Fatalf("regular snapshot should still read HEAD skill hash = %q, want %q", regularSnapshot.SkillHash, snapshot.SkillHash)
	}
	appliedSnapshot, err := buildPromptEvaluationSkillAppliedWorktreeSnapshot(CreatePromptEvaluationSkillSnapshotRequest{
		RepoPath:  repoPath,
		Branch:    "HEAD",
		SkillPath: skillPath,
	}, *applyEvidence, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("build applied worktree snapshot: %v", err)
	}
	if appliedSnapshot.SkillHash != result.SkillHashAfter || appliedSnapshot.SkillHash == snapshot.SkillHash {
		t.Fatalf("applied snapshot hash = %q, apply after = %q, source = %q", appliedSnapshot.SkillHash, result.SkillHashAfter, snapshot.SkillHash)
	}
	appliedSkill, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(skillPath)))
	if err != nil {
		t.Fatalf("read applied skill: %v", err)
	}
	if string(appliedSkill) != v2 {
		t.Fatalf("applied skill = %q, want %q", string(appliedSkill), v2)
	}
	changelogPath := filepath.Join(repoPath, ".codebuddy/skills/05-verify/CHANGELOG.md")
	changelog, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatalf("read changelog: %v", err)
	}
	changelogText := string(changelog)
	for _, want := range []string{
		"Skill optimization candidate",
		snapshot.BaseCommit,
		"Improve verification evidence discipline.",
		"Focused helper test passed.",
		"Reverse the candidate patch.",
	} {
		if !strings.Contains(changelogText, want) {
			t.Fatalf("changelog missing %q:\n%s", want, changelogText)
		}
	}
}

func TestPromptEvaluationSkillPatchDefaultsRequests(t *testing.T) {
	snapshot := PromptEvaluationSkillSnapshotResponse{
		SchemaVersion:    promptEvaluationSkillSnapshotSchema,
		Provider:         "local_directory",
		Repo:             "goal-test fixture",
		RepoPath:         "/tmp/goal-d-skill-patch",
		Branch:           "HEAD",
		BaseCommit:       "base-commit",
		SkillPath:        ".codebuddy/skills/05-verify/SKILL.md",
		SkillHash:        "hash-before",
		SnapshotTime:     "2026-06-25T16:00:00Z",
		SourceResourceID: "resource-1",
	}
	patchText := "diff --git a/.codebuddy/skills/05-verify/SKILL.md b/.codebuddy/skills/05-verify/SKILL.md\n"
	candidate := db.PromptEvaluationOptimizationCandidate{
		Metrics: mustSkillTestJSON(t, map[string]any{
			"skill_patch": map[string]any{
				"schema_version":       promptEvaluationSkillPatchSchema,
				"patch":                patchText,
				"source_snapshot":      snapshot,
				"source_resource_id":   "resource-1",
				"repo_path":            "/tmp/goal-d-skill-patch",
				"target_branch":        "HEAD",
				"skill_path":           ".codebuddy/skills/05-verify/SKILL.md",
				"changelog_path":       ".codebuddy/skills/05-verify/CHANGELOG.md",
				"expected_improvement": "Improve verification evidence discipline.",
				"verification_plan":    "Run re-eval after apply.",
				"publication_status":   "draft",
			},
		}),
	}
	skillPatch := skillPatchFromCandidate(candidate)
	if skillPatch == nil || skillPatch.Patch != patchText || skillPatch.SourceSnapshot == nil {
		t.Fatalf("skill patch decode = %+v", skillPatch)
	}
	if skillPatch.PatchHash == "" || skillPatch.PatchBytes == 0 {
		t.Fatalf("skill patch hash/bytes should be present after decode = %+v", skillPatch)
	}
	if skillPatch.CandidateIntent != "update_existing_skill" {
		t.Fatalf("legacy skill patch should default to update_existing_skill, got %+v", skillPatch)
	}
	freshnessReq := CheckPromptEvaluationSkillFreshnessRequest{}
	applySkillPatchFreshnessDefaults(&freshnessReq, skillPatch)
	if freshnessReq.CandidatePatch != patchText || freshnessReq.SourceResourceID != "resource-1" || freshnessReq.SkillPath != snapshot.SkillPath {
		t.Fatalf("freshness defaults = %+v", freshnessReq)
	}
	applyReq := ApplyPromptEvaluationSkillCandidateRequest{}
	applySkillPatchApplyDefaults(&applyReq, skillPatch)
	if applyReq.CandidatePatch != patchText || applyReq.ChangeReason == "" || applyReq.VerificationResult == "" || applyReq.ChangelogPath == "" {
		t.Fatalf("apply defaults = %+v", applyReq)
	}
	reEvalReq := PreparePromptEvaluationSkillReEvalRequest{}
	applySkillPatchReEvalDefaults(&reEvalReq, skillPatch)
	if reEvalReq.SourceResourceID != "resource-1" || reEvalReq.RepoPath == "" || reEvalReq.TargetBranch != "HEAD" || reEvalReq.SkillPath != snapshot.SkillPath {
		t.Fatalf("re-eval defaults = %+v", reEvalReq)
	}

	operationPatch, err := normalizePromptEvaluationSkillPatch(PromptEvaluationSkillPatch{
		Patch:                patchText,
		SourceSnapshot:       &snapshot,
		CandidateIntent:      "create_operation_skill",
		OperationSkillKey:    "user-center/add-api",
		OperationSkillPath:   ".codebuddy/skills/add-api/SKILL.md",
		OperationSkillReason: "Repeated benchmark failures show this project action needs a dedicated operation skill.",
	}, db.PromptEvaluationOptimizationCandidate{}, time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("normalize operation skill patch: %v", err)
	}
	if operationPatch.CandidateIntent != "create_operation_skill" || operationPatch.OperationSkillKey != "user-center/add-api" || operationPatch.OperationSkillPath == "" || operationPatch.OperationSkillReason == "" {
		t.Fatalf("operation skill patch fields not preserved: %+v", operationPatch)
	}
}

func TestPromptEvaluationSkillApplyBlocksDirtyWorktreeByDefault(t *testing.T) {
	repoPath := t.TempDir()
	runSkillTestGit(t, repoPath, "init")
	runSkillTestGit(t, repoPath, "config", "user.email", "test@example.com")
	runSkillTestGit(t, repoPath, "config", "user.name", "Test User")

	skillPath := ".codebuddy/skills/05-verify/SKILL.md"
	v1 := "# Verify\n\n- Run focused checks.\n"
	writeSkillTestFile(t, repoPath, skillPath, v1)
	runSkillTestGit(t, repoPath, "add", skillPath)
	runSkillTestGit(t, repoPath, "commit", "-m", "add verify skill")

	now := time.Date(2026, 6, 25, 14, 0, 0, 0, time.UTC)
	snapshot, err := buildPromptEvaluationSkillSnapshot(CreatePromptEvaluationSkillSnapshotRequest{
		RepoPath:  repoPath,
		Branch:    "HEAD",
		SkillPath: skillPath,
	}, now)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	v2 := "# Verify\n\n- Run focused checks.\n- Attach ledger references.\n"
	writeSkillTestFile(t, repoPath, skillPath, v2)
	patch := runSkillTestGit(t, repoPath, "diff", "--", skillPath)
	writeSkillTestFile(t, repoPath, skillPath, v1)
	writeSkillTestFile(t, repoPath, "README.md", "dirty\n")

	result, err := applyPromptEvaluationSkillCandidate(ApplyPromptEvaluationSkillCandidateRequest{
		RepoPath:       repoPath,
		SkillPath:      skillPath,
		CandidatePatch: patch,
	}, snapshot, nil, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("apply dirty worktree: %v", err)
	}
	if result.Status != "blocked" || !strings.Contains(result.Reason, "uncommitted changes") {
		t.Fatalf("dirty worktree result = %+v", result)
	}
	if len(result.ChangedFiles) == 0 {
		t.Fatalf("dirty worktree did not report changed files")
	}
}

func TestPromptEvaluationSkillReEvalPayloadUsesApprovedDraftsAndSnapshot(t *testing.T) {
	snapshot := PromptEvaluationSkillSnapshotResponse{
		SchemaVersion: promptEvaluationSkillSnapshotSchema,
		Provider:      "gongfeng",
		Repo:          "example/goal-test",
		Branch:        "v5.0.0_dev_sop",
		BaseCommit:    "abc123456789",
		SkillPath:     ".codebuddy/skills/05-verify/SKILL.md",
		SkillHash:     "hash-after",
		SnapshotTime:  "2026-06-25T15:00:00Z",
	}
	drafts := []PromptEvaluationSkillCaseDraft{
		{
			SchemaVersion:       promptEvaluationSkillCaseDraftSchema,
			Status:              "approved",
			Input:               "Verify acceptance evidence.",
			ExpectedBehavior:    "Evidence is recorded before completion.",
			Verification:        "Run focused checks.",
			EvidenceSource:      "git:commit-a:.codebuddy/skills/05-verify/SKILL.md",
			ApplicableSkillHash: "hash-before",
			ApplicableScope:     ".codebuddy/skills/05-verify/SKILL.md",
			SourceCommit:        "commit-a",
			CommitSubject:       "require evidence",
			SkillPath:           ".codebuddy/skills/05-verify/SKILL.md",
		},
		{
			SchemaVersion:       promptEvaluationSkillCaseDraftSchema,
			Status:              "draft",
			Input:               "Draft case.",
			ExpectedBehavior:    "Draft behavior.",
			Verification:        "Draft verification.",
			EvidenceSource:      "git:commit-b:.codebuddy/skills/05-verify/SKILL.md",
			ApplicableSkillHash: "hash-draft",
			ApplicableScope:     ".codebuddy/skills/05-verify/SKILL.md",
			SourceCommit:        "commit-b",
			CommitSubject:       "draft evidence",
			SkillPath:           ".codebuddy/skills/05-verify/SKILL.md",
		},
	}
	cases := buildPromptEvaluationSkillReEvalCases(drafts, snapshot, PreparePromptEvaluationSkillReEvalRequest{})
	if len(cases) != 1 {
		t.Fatalf("case count = %d, want approved-only default", len(cases))
	}
	if cases[0].Variables["re_eval_skill_hash"] != "hash-after" {
		t.Fatalf("case variables = %+v", cases[0].Variables)
	}
	if !strings.Contains(strings.Join(cases[0].ExpectedContains, "\n"), "Evidence is recorded") {
		t.Fatalf("expected_contains = %+v", cases[0].ExpectedContains)
	}
	withDrafts := buildPromptEvaluationSkillReEvalCases(drafts, snapshot, PreparePromptEvaluationSkillReEvalRequest{IncludeDraft: true})
	if len(withDrafts) != 2 {
		t.Fatalf("case count with drafts = %d, want 2", len(withDrafts))
	}
	payload := buildPromptEvaluationSkillReEvalPayload(
		dbPromptEvaluationAssetForSkillTest(),
		dbPromptEvaluationCandidateForSkillTest(),
		snapshot,
		snapshot,
		cases,
	)
	if payload["skill_re_eval_contract"] != "multica.skill.re_eval.v1" || payload["re_eval_required"] != true {
		t.Fatalf("payload contract = %+v", payload)
	}
	payloadCases, ok := payload["cases"].([]map[string]any)
	if !ok || len(payloadCases) != 1 {
		t.Fatalf("payload cases = %#v", payload["cases"])
	}
}

func TestPromptEvaluationSkillReEvalRunHelpersValidateAssetAndEvidence(t *testing.T) {
	candidate := dbPromptEvaluationCandidateForSkillTest()
	reEvalAssetID := "33333333-3333-4333-8333-333333333333"
	candidate.Metrics = mustJSONBytes(map[string]any{
		"skill_re_eval": map[string]any{
			"asset_id": reEvalAssetID,
		},
	})
	if got := skillReEvalAssetIDFromCandidate(candidate); got != reEvalAssetID {
		t.Fatalf("re-eval asset id = %q, want %q", got, reEvalAssetID)
	}

	snapshot := PromptEvaluationSkillSnapshotResponse{
		SchemaVersion: promptEvaluationSkillSnapshotSchema,
		Provider:      "gongfeng",
		Repo:          "example/goal-test",
		Branch:        "v5.0.0_dev_sop",
		BaseCommit:    "abc123456789",
		SkillPath:     ".codebuddy/skills/05-verify/SKILL.md",
		SkillHash:     "hash-after",
		SnapshotTime:  "2026-06-25T15:30:00Z",
	}
	asset := dbPromptEvaluationAssetForSkillTest()
	asset.ID = parseUUID(reEvalAssetID)
	asset.AssetType = promptEvaluationAssetTestSuite
	payload := map[string]any{
		"skill_re_eval_contract": "multica.skill.re_eval.v1",
		"source_candidate_id":    uuidToString(candidate.ID),
		"source_skill_snapshot":  snapshot,
		"re_eval_snapshot":       snapshot,
	}
	if err := validatePromptEvaluationSkillReEvalAsset(candidate, asset, payload); err != nil {
		t.Fatalf("validate re-eval asset: %v", err)
	}
	sourceSnapshot, reEvalSnapshot := skillSnapshotsFromReEvalPayload(payload)
	if sourceSnapshot.SkillHash != "hash-after" || reEvalSnapshot.SkillHash != "hash-after" {
		t.Fatalf("snapshots = source %+v re-eval %+v", sourceSnapshot, reEvalSnapshot)
	}

	otherCandidate := candidate
	otherCandidate.ID = parseUUID("44444444-4444-4444-8444-444444444444")
	if err := validatePromptEvaluationSkillReEvalAsset(otherCandidate, asset, payload); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("wrong candidate validation error = %v", err)
	}

	run := db.PromptEvaluationRun{
		ID:      parseUUID("55555555-5555-4555-8555-555555555555"),
		Status:  "通过",
		RunKind: "本地渲染",
	}
	evidence := buildPromptEvaluationSkillReEvalRunEvidence(candidate, asset, run, promptEvaluationRunResult{
		PassedCases: 2,
		FailedCases: 0,
		PassRate:    1,
	}, 2)
	if evidence["run_id"] != uuidToString(run.ID) || evidence["proof_scope"] != "local_prompt_evaluation_run" {
		t.Fatalf("run evidence identity = %+v", evidence)
	}
	if evidence["run_kind"] != "模板渲染检查" || evidence["case_count"] != 2 || evidence["pass_rate"] != float64(1) {
		t.Fatalf("run evidence metrics = %+v", evidence)
	}
	if !strings.Contains(stringFromAny(evidence["proof_boundary"]), "Gongfeng/agent skill runtime") {
		t.Fatalf("run evidence boundary = %+v", evidence)
	}
}

func runSkillTestGit(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	if len(args) > 0 && args[0] == "diff" {
		return string(out)
	}
	return strings.TrimSpace(string(out))
}

func writeSkillTestFile(t *testing.T, repoPath string, relativePath string, content string) {
	t.Helper()
	fullPath := filepath.Join(repoPath, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(fullPath), err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", fullPath, err)
	}
}

func mustSkillTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func dbPromptEvaluationAssetForSkillTest() db.PromptEvaluationAsset {
	return db.PromptEvaluationAsset{
		ID:          parseUUID("11111111-1111-4111-8111-111111111111"),
		WorkspaceID: parseUUID("22222222-2222-4222-8222-222222222222"),
	}
}

func dbPromptEvaluationCandidateForSkillTest() db.PromptEvaluationOptimizationCandidate {
	return db.PromptEvaluationOptimizationCandidate{
		ID:          parseUUID("33333333-3333-4333-8333-333333333333"),
		WorkspaceID: parseUUID("22222222-2222-4222-8222-222222222222"),
		AssetID:     parseUUID("11111111-1111-4111-8111-111111111111"),
		RunID:       parseUUID("44444444-4444-4444-8444-444444444444"),
	}
}
