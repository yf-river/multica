import { test, expect } from "@playwright/test";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { TestApiClient } from "./fixtures";

const execFileAsync = promisify(execFile);
const E2E_WORKER = process.env.TEST_PARALLEL_INDEX ?? process.env.TEST_WORKER_INDEX ?? "0";
const E2E_RUN_ID = process.env.E2E_RUN_ID ?? `${Date.now().toString(36)}-${process.pid.toString(36)}`;
const E2E_ACCOUNT = process.env.E2E_ACCOUNT ?? `e2e-goal-e-writeback-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_NAME = process.env.E2E_NAME ?? "Goal E Gongfeng Writeback User";
const E2E_WORKSPACE = process.env.E2E_WORKSPACE ?? `e2e-goal-e-writeback-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_WORKSPACE_NAME = process.env.E2E_WORKSPACE_NAME ?? `Goal E Gongfeng Writeback ${E2E_WORKER}`;
const GONGFENG_URL = "https://git.code.tencent.com/ChainWeaver/ida/user-center";
const WRITEBACK_REPO = process.env.GOAL_E_GONGFENG_WRITEBACK_REPO ?? "";
const WRITEBACK_SKILL_PATH = process.env.GOAL_E_GONGFENG_WRITEBACK_SKILL_PATH ?? ".codebuddy/skills/sop.eval/SKILL.md";
const WRITEBACK_CHANGELOG_PATH = process.env.GOAL_E_GONGFENG_WRITEBACK_CHANGELOG_PATH ?? path.posix.join(path.posix.dirname(WRITEBACK_SKILL_PATH), "CHANGELOG.md");

test.describe("Goal E Gongfeng controlled skill writeback", () => {
  test.describe.configure({ timeout: 180_000 });

  test.skip(!WRITEBACK_REPO, "Set GOAL_E_GONGFENG_WRITEBACK_REPO to opt in to controlled Gongfeng checkout writeback.");

  test("applies skill candidate to v5.0.0_dev_sop checkout, writes CHANGELOG, and re-evals", async () => {
    const api = new TestApiClient();
    const artifactPrefix = `GoalE Gongfeng Writeback ${Date.now()}`;
    const skillPath = WRITEBACK_SKILL_PATH;
    const changelogPath = WRITEBACK_CHANGELOG_PATH;
    let tempDir: string | null = null;
    try {
      await api.login(E2E_ACCOUNT, E2E_NAME);
      const workspace = await api.ensureWorkspace(E2E_WORKSPACE_NAME, E2E_WORKSPACE);
      await api.markUserOnboarded();
      await api.cleanupPromptArtifactsByPrefix("GoalE Gongfeng Writeback");

      const branch = (await git(WRITEBACK_REPO, ["branch", "--show-current"])).trim();
      expect(branch).toBe("v5.0.0_dev_sop");
      const headCommit = (await git(WRITEBACK_REPO, ["rev-parse", "HEAD"])).trim();
      const preExistingDirty = (await git(WRITEBACK_REPO, ["status", "--short"])).trim().split(/\r?\n/).filter(Boolean);
      expect(preExistingDirty.some((item) => item.includes(skillPath) || item.includes(changelogPath))).toBe(false);

      const project = await api.createProject(`${artifactPrefix} project`);
      const gongfengResource = await api.createProjectResource(project.id, {
        resource_type: "gongfeng_repo",
        label: `${artifactPrefix} user-center v5.0.0_dev_sop`,
        resource_ref: {
          provider: "gongfeng",
          url: `${GONGFENG_URL}/commits/v5.0.0_dev_sop`,
          project_path: "ChainWeaver/ida/user-center",
          ref: "v5.0.0_dev_sop",
          resource_kind: "branch",
          title: "user-center",
        },
      });
      const prompt = await api.createPromptForE2E(artifactPrefix, {
        name: `${artifactPrefix} prompt`,
        content: "{{expected_behavior}}\n{{verification}}\n{{evidence_source}}",
        variables: [
          { name: "expected_behavior", label: "Expected behavior", required: false },
          { name: "verification", label: "Verification", required: false },
          { name: "evidence_source", label: "Evidence source", required: false },
        ],
      });
      const asset = await api.createPromptEvaluationAsset({
        prompt_id: prompt.id,
        name: `${artifactPrefix} asset`,
        description: "Goal E controlled Gongfeng checkout writeback fixture",
        asset_type: "测试套件",
        payload: {
          cases: [{
            名称: "force controlled writeback optimizer candidate",
            变量: {},
            期望包含: ["__missing_goal_e_gongfeng_writeback_marker__"],
          }],
        },
        status: "启用",
      });
      const inventory = await api.createPromptEvaluationSkillInventory(asset.id, {
        source_resource_id: gongfengResource.id,
        repo_path: WRITEBACK_REPO,
        skill_root: ".codebuddy/skills",
      });
      const snapshotResult = await api.createPromptEvaluationSkillSnapshot(asset.id, {
        source_resource_id: gongfengResource.id,
        repo_path: WRITEBACK_REPO,
        skill_path: skillPath,
      });
      const snapshot = snapshotResult.snapshot;
      expect(snapshot.provider).toBe("gongfeng");
      expect(snapshot.repo).toBe("ChainWeaver/ida/user-center");
      expect(snapshot.branch).toBe("v5.0.0_dev_sop");

      const draftsResult = await api.createPromptEvaluationSkillCaseDrafts(asset.id, {
        repo_path: WRITEBACK_REPO,
        skill_path: skillPath,
        limit: 3,
        auto_approve: true,
      });
      await api.updatePromptLibraryItem(prompt.id, {
        content: draftsResult.drafts
          .map((draft: { expected_behavior: string; verification: string; evidence_source: string }) => [
            draft.expected_behavior,
            draft.verification,
            draft.evidence_source,
          ].join("\n"))
          .join("\n\n"),
        tags: ["E2E", "Goal E", "Gongfeng writeback"],
        status: "启用",
      });
      await api.runPromptEvaluationAsset(asset.id);
      const failedRun = await expect
        .poll(async () => {
          const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id });
          return runs.find((run) => run.status === "未通过") ?? null;
        }, { timeout: 15_000 })
        .not.toBeNull()
        .then(async () => (await api.listPromptEvaluationRuns({ asset_id: asset.id })).find((run) => run.status === "未通过")!);
      const candidate = await api.createPromptEvaluationOptimizationCandidate(failedRun.id);
      const marker = `- Goal E controlled Gongfeng writeback evidence: ${E2E_RUN_ID}.`;
      const candidatePatch = await createAppendPatch(WRITEBACK_REPO, skillPath, marker);
      const updatedCandidate = await api.updatePromptEvaluationOptimizationCandidate(candidate.id, {
        candidate_name: `${artifactPrefix} controlled Gongfeng skill patch`,
        candidate_content: "Controlled Gongfeng writeback patch is stored in skill_patch.patch.",
        rationale: `Patch records Goal E controlled Gongfeng writeback evidence in ${skillPath}.`,
        edit_note: "Goal E E-06 fixture applies to the actual v5.0.0_dev_sop checkout with pre-existing dirty files recorded.",
        skill_patch: {
          patch: candidatePatch,
          source_snapshot: snapshot,
          source_resource_id: gongfengResource.id,
          repo_path: WRITEBACK_REPO,
          target_branch: "v5.0.0_dev_sop",
          skill_path: skillPath,
          changelog_path: changelogPath,
          expected_improvement: "Prove the final skill chain can write a reviewed candidate into a clean controlled Gongfeng checkout.",
          risk: `Scoped append to ${skillPath} plus generated CHANGELOG entry; unrelated pre-existing dirty files are recorded and not modified by this fixture.`,
          verification_plan: "Run freshness, apply, prepare re-eval, run re-eval, and inspect changed skill/CHANGELOG files.",
          publication_status: "controlled_writeback",
        },
      });
      const freshness = await api.checkPromptEvaluationSkillCandidateFreshness(candidate.id, {
        source_resource_id: gongfengResource.id,
        repo_path: WRITEBACK_REPO,
      });
      expect(["fresh", "rebaseable"]).toContain(freshness.status);
      const apply = await api.applyPromptEvaluationSkillCandidate(candidate.id, {
        source_resource_id: gongfengResource.id,
        repo_path: WRITEBACK_REPO,
        allow_dirty: preExistingDirty.length > 0,
        rollback_plan: `Reverse candidate patch ${updatedCandidate.skill_patch?.patch_hash} and remove the generated CHANGELOG entry.`,
      });
      expect(apply.apply.status, JSON.stringify(apply.apply, null, 2)).toBe("applied");
      expect(apply.apply.changed_files.some((item: string) => item.includes(skillPath))).toBeTruthy();
      expect(apply.apply.changed_files.some((item: string) => item.includes(changelogPath))).toBeTruthy();
      const reEvalAsset = await api.preparePromptEvaluationSkillReEvalAsset(candidate.id, {
        source_resource_id: gongfengResource.id,
        repo_path: WRITEBACK_REPO,
        snapshot,
        include_draft: false,
      });
      const reEvalRun = await api.runPromptEvaluationSkillReEval(candidate.id, {
        asset_id: reEvalAsset.asset.id,
      });
      expect(reEvalRun.run.status, JSON.stringify(reEvalRun.run, null, 2)).toBe("通过");
      const postDirty = (await git(WRITEBACK_REPO, ["status", "--short"])).trim().split(/\r?\n/).filter(Boolean);

      const artifactDir = path.resolve(process.cwd(), "artifacts/acceptance");
      await mkdir(artifactDir, { recursive: true });
      const artifact = {
        schema: "multica.goal_e.gongfeng_skill_writeback.v1",
        generated_at: new Date().toISOString(),
        ok: true,
        workspace: { id: workspace.id, slug: workspace.slug },
        project_id: project.id,
        resource: {
          id: gongfengResource.id,
          resource_type: "gongfeng_repo",
          provider: "gongfeng",
          project_path: "ChainWeaver/ida/user-center",
          branch: "v5.0.0_dev_sop",
          url: GONGFENG_URL,
        },
        repo_path: WRITEBACK_REPO,
        head_commit_before: headCommit,
        pre_existing_dirty: preExistingDirty,
        post_apply_dirty: postDirty,
        asset_id: asset.id,
        failed_run_id: failedRun.id,
        candidate_id: candidate.id,
        inventory_count: inventory.inventory.discovered_count,
        snapshot,
        draft_count: draftsResult.created_count,
        freshness: { status: freshness.status, patch_check: freshness.patch_check, head_commit: freshness.head_commit },
        apply: {
          status: apply.apply.status,
          skill_hash_before: apply.apply.skill_hash_before,
          skill_hash_after: apply.apply.skill_hash_after,
          changelog_path: apply.apply.changelog_path,
          changed_files: apply.apply.changed_files,
          patch_check: apply.apply.patch_check,
        },
        re_eval_asset_id: reEvalAsset.asset.id,
        re_eval_run_id: reEvalRun.run.id,
        re_eval_status: reEvalRun.run.status,
        re_eval_proof_scope: reEvalRun.re_eval_run.proof_scope,
        proof_boundary: "Controlled local writeback to a clean clone of the actual user-center v5.0.0_dev_sop Gongfeng branch; no remote MR was created.",
      };
      const artifactPath = path.join(artifactDir, `goal-e-gongfeng-skill-writeback-${Date.now()}.json`);
      await writeFile(artifactPath, `${JSON.stringify(artifact, null, 2)}\n`);
    } finally {
      await api.cleanup();
      if (tempDir) await rm(tempDir, { recursive: true, force: true });
    }
  });
});

async function createAppendPatch(repoPath: string, relativePath: string, line: string) {
  const current = await readFile(path.join(repoPath, relativePath), "utf8");
  const next = `${current}${current.endsWith("\n") ? "" : "\n"}${line}\n`;
  const tempDir = await mkdtemp(path.join(os.tmpdir(), "goal-e-gongfeng-writeback-"));
  const tempFile = path.join(tempDir, "SKILL.md");
  await writeFile(tempFile, next);
  try {
    const diff = await gitDiffNoIndex(repoPath, relativePath, tempFile);
    return diff
      .replace(/^diff --git a\/.+ b\/.+$/m, `diff --git a/${relativePath} b/${relativePath}`)
      .replace(/^\+\+\+ b\/.+$/m, `+++ b/${relativePath}`);
  } finally {
    await rm(tempDir, { recursive: true, force: true });
  }
}

async function gitDiffNoIndex(repoPath: string, relativePath: string, newFile: string) {
  try {
    return await git(repoPath, ["diff", "--no-index", "--", relativePath, newFile]);
  } catch (error) {
    const maybe = error as { stdout?: string; code?: number };
    if (maybe.code === 1 && maybe.stdout) return maybe.stdout;
    throw error;
  }
}

async function git(repoPath: string, args: string[]) {
  const result = await execFileAsync("git", args, { cwd: repoPath });
  return result.stdout;
}
