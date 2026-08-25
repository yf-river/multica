import { test, expect } from "@playwright/test";
import { loginAsDefault, createTestApi, waitForPageText } from "./helpers";
import { TestApiClient } from "./fixtures";
import {
  DEFAULT_E2E_ACCOUNT,
  DEFAULT_E2E_NAME,
  DEFAULT_E2E_PASSWORD,
  DEFAULT_E2E_WORKSPACE,
  DEFAULT_E2E_WORKSPACE_NAME,
} from "./test-identity";

/**
 * Closed-loop Inbox journey:
 * second member assigns unique-named issue to primary → inbox item appears →
 * UI select (auto mark-read) → archive → public API readback.
 */
test.describe("Inbox closed loop", () => {
  test("assignment notification mark-read and archive with API readback", async ({ page }) => {
    test.setTimeout(120_000);

    const primary = await createTestApi();
    await primary.login(DEFAULT_E2E_ACCOUNT, DEFAULT_E2E_NAME, DEFAULT_E2E_PASSWORD);
    const workspace = await primary.ensureWorkspace(
      DEFAULT_E2E_WORKSPACE_NAME,
      DEFAULT_E2E_WORKSPACE,
    );
    const primaryUserId = primary.getUserId();
    expect(primaryUserId).toBeTruthy();

    const suffix = Date.now();
    const actorAccount = `inbox_actor_${suffix}`;
    const actorPassword = "InboxActor1!";
    const issueTitle = `E2E Inbox Assign ${suffix}`;

    let actor: TestApiClient | null = null;

    try {
      // Public API: create a second workspace member who can assign to primary
      await primary.createWorkspaceMember(workspace.id, {
        account: actorAccount,
        name: `Inbox Actor ${suffix}`,
        password: actorPassword,
        role: "member",
      });

      actor = new TestApiClient();
      await actor.login(actorAccount, `Inbox Actor ${suffix}`, actorPassword);
      actor.setWorkspaceId(workspace.id);
      actor.setWorkspaceSlug(workspace.slug);

      const issue = await actor.createIssue(issueTitle, {
        status: "todo",
        priority: "high",
        assignee_type: "member",
        assignee_id: primaryUserId,
      });
      // Track for primary cleanup too
      primary.rememberIssue(issue.id);

      // Poll public inbox API until assignment notification arrives
      let inboxItem: { id: string; title: string; read: boolean; archived: boolean } | null =
        null;
      await expect
        .poll(
          async () => {
            const items = await primary.listInbox();
            const hit = items.find(
              (item) =>
                item.title === issueTitle ||
                item.issue_id === issue.id ||
                (item.title && item.title.includes(String(suffix))),
            );
            if (hit) {
              inboxItem = hit;
              return hit.id;
            }
            return "";
          },
          { timeout: 30000, message: "wait for assignment inbox item" },
        )
        .not.toBe("");
      expect(inboxItem).toBeTruthy();
      expect(inboxItem!.read).toBe(false);

      // Browser as primary user
      const workspaceSlug = await loginAsDefault(page);
      await page.goto(`/${workspaceSlug}/inbox`, { waitUntil: "domcontentloaded" });
      await waitForPageText(page, "收件箱", 60000);

      // Unique issue title should appear in inbox list
      const row = page.getByText(issueTitle).first();
      await expect(row).toBeVisible({ timeout: 30000 });
      await row.click();

      // Selecting auto mark-read — poll API
      await expect
        .poll(
          async () => {
            const items = await primary.listInbox();
            const hit = items.find((item) => item.id === inboxItem!.id);
            return hit?.read === true;
          },
          { timeout: 15000, message: "inbox item marked read after select" },
        )
        .toBe(true);

      // Archive via detail button
      await page.getByRole("button", { name: "归档", exact: true }).click();

      await expect
        .poll(
          async () => {
            const items = await primary.listInbox();
            // Archived items leave the default list
            return items.some((item) => item.id === inboxItem!.id);
          },
          { timeout: 15000, message: "archived item removed from inbox list" },
        )
        .toBe(false);

      // Optional: archive via API should be idempotent / 404 ok — primary archive readback
      // Item gone from list is the public contract for archive.
    } finally {
      if (actor) {
        await actor.cleanup().catch(() => undefined);
      }
      await primary.cleanup().catch(() => undefined);
    }
  });
});
