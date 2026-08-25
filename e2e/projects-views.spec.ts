import { test, expect, type Page } from "@playwright/test";
import { loginAsDefault, createTestApi, waitForPageText } from "./helpers";
import type { TestApiClient } from "./fixtures";

/**
 * Closed-loop Project Board / List / Swimlane journey.
 * Fixture via public API → open project detail → exercise board/list/swimlane → public API readback.
 *
 * View modes are applied via the persisted project issue view store key and
 * verified in the real project-detail UI. A live menu switch board→list is
 * also exercised (same path as e2e/issues.spec.ts). Repeated Base UI menu
 * opens on this surface remount mid-animation under Playwright; persist+reload
 * still loads the shipped ProjectIssuesContent branch for each mode.
 */
test.describe("Project Board/List/Swimlane", () => {
  test.describe.configure({ timeout: 180_000 });

  let api: TestApiClient;
  let workspaceSlug: string;

  test.beforeEach(async ({ page }) => {
    api = await createTestApi();
    workspaceSlug = await loginAsDefault(page);
  });

  test.afterEach(async () => {
    if (api) {
      await api.cleanup();
    }
  });

  async function setProjectIssueViewMode(page: Page, mode: "board" | "list" | "swimlane") {
    await page.evaluate((nextMode) => {
      const keys = Object.keys(localStorage).filter((key) =>
        key.includes("project_issues_view"),
      );
      if (keys.length === 0) {
        // Seed zustand-persist shape used by createIssueViewStore
        localStorage.setItem(
          "project_issues_view",
          JSON.stringify({ state: { viewMode: nextMode }, version: 0 }),
        );
        return;
      }
      for (const key of keys) {
        const raw = localStorage.getItem(key);
        if (!raw) continue;
        try {
          const parsed = JSON.parse(raw) as {
            state?: { viewMode?: string };
            viewMode?: string;
          };
          if (parsed && typeof parsed === "object" && parsed.state) {
            parsed.state.viewMode = nextMode;
          } else if (parsed && typeof parsed === "object") {
            parsed.viewMode = nextMode;
          }
          localStorage.setItem(key, JSON.stringify(parsed));
        } catch {
          localStorage.setItem(
            key,
            JSON.stringify({ state: { viewMode: nextMode }, version: 0 }),
          );
        }
      }
    }, mode);
  }

  test("create project issue then switch board list swimlane with API readback", async ({
    page,
  }) => {
    const suffix = Date.now();
    const projectTitle = `E2E Project Views ${suffix}`;
    const issueTitle = `E2E Project View Issue ${suffix}`;

    const project = await api.createProject(projectTitle);
    const issue = await api.createIssue(issueTitle, {
      project_id: project.id,
      status: "todo",
      priority: "medium",
    });

    const projectReadback = await api.getProject(project.id);
    expect(projectReadback.id).toBe(project.id);
    expect(projectReadback.title).toBe(projectTitle);
    const issueReadback = await api.getIssue(issue.id);
    expect(issueReadback.project_id).toBe(project.id);
    expect(issueReadback.title).toBe(issueTitle);

    const projectUrl = `/${workspaceSlug}/projects/${project.id}`;

    // --- Board (default) ---
    await page.goto(projectUrl, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, projectTitle, 60000);
    await expect(page.getByText(projectTitle).first()).toBeVisible({ timeout: 30000 });
    await expect(page.getByText(issueTitle).first()).toBeVisible({ timeout: 30000 });
    await expect(page.getByText("待办").first()).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("button", { name: "看板", exact: true })).toBeVisible({
      timeout: 15000,
    });

    // Live UI menu switch board → list (proven pattern from issues.spec)
    await page.getByRole("button", { name: "看板", exact: true }).click();
    await page.getByRole("menuitemradio", { name: "列表", exact: true }).click();
    await expect(page.getByRole("button", { name: "列表", exact: true })).toBeVisible({
      timeout: 15000,
    });
    await expect(page.getByText(issueTitle).first()).toBeVisible({ timeout: 15000 });

    // --- Swimlane via shipped persist path + real project detail render ---
    await setProjectIssueViewMode(page, "swimlane");
    await page.goto(projectUrl, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, issueTitle, 60000);
    await expect(page.getByRole("button", { name: "泳道", exact: true })).toBeVisible({
      timeout: 20000,
    });
    await expect(page.getByText(issueTitle).first()).toBeVisible({ timeout: 15000 });
    await expect(
      page.getByText(/无父级|其他父级|未指派|无项目|无负责人/).first(),
    ).toBeVisible({ timeout: 15000 });

    // --- Board again via persist ---
    await setProjectIssueViewMode(page, "board");
    await page.goto(projectUrl, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, issueTitle, 60000);
    await expect(page.getByRole("button", { name: "看板", exact: true })).toBeVisible({
      timeout: 20000,
    });
    await expect(page.getByText("待办").first()).toBeVisible({ timeout: 15000 });
    await expect(page.getByText(issueTitle).first()).toBeVisible({ timeout: 15000 });

    // --- List again via persist ---
    await setProjectIssueViewMode(page, "list");
    await page.goto(projectUrl, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, issueTitle, 60000);
    await expect(page.getByRole("button", { name: "列表", exact: true })).toBeVisible({
      timeout: 20000,
    });
    await expect(page.getByText(issueTitle).first()).toBeVisible({ timeout: 15000 });

    const finalProject = await api.getProject(project.id);
    expect(finalProject.title).toBe(projectTitle);
    const finalIssue = await api.getIssue(issue.id);
    expect(finalIssue.project_id).toBe(project.id);
    expect(finalIssue.title).toBe(issueTitle);
  });
});
