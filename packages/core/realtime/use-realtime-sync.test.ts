import { QueryClient } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { inboxKeys } from "../inbox/queries";
import { notificationPreferenceKeys } from "../notification-preferences/queries";
import { workspaceKeys } from "../workspace/queries";
import {
  DEFAULT_WORKSPACE_SETTINGS,
  type InboxItem,
  type Workspace,
} from "../types";
import { handleInboxNew } from "./use-realtime-sync";

function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false },
    },
  });
}

describe("handleInboxNew", () => {
  function workspace(overrides: Partial<Workspace> = {}): Workspace {
    return {
      id: "ws-a",
      name: "Workspace A",
      slug: "workspace-a",
      description: null,
      context: null,
      settings: { ...DEFAULT_WORKSPACE_SETTINGS },
      repos: [],
      issue_prefix: "WSA",
      avatar_url: null,
      ...overrides,
    };
  }

  function inboxItem(overrides: Partial<InboxItem> = {}): InboxItem {
    return {
      id: "item-1",
      workspace_id: "ws-a",
      recipient_type: "member",
      recipient_id: "member-1",
      actor_type: "member",
      actor_id: "member-2",
      type: "mentioned",
      issue_id: "issue-1",
      title: "Mentioned you",
      body: "in a comment",
      issue_status: null,
      read: false,
      archived: false,
      created_at: "2026-05-18T00:00:00Z",
      details: null,
      ...overrides,
    };
  }

  function stubDesktopAPI() {
    const showNotification = vi.fn();
    (globalThis as Record<string, unknown>).desktopAPI = { showNotification };
    return showNotification;
  }

  afterEach(() => {
    delete (globalThis as Record<string, unknown>).desktopAPI;
  });

  it("still shows the banner when the slug can't be resolved, with an empty slug so the click is a no-op", async () => {
    const qc = createQueryClient();
    // Workspace list is cached but doesn't contain the item's workspace.
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [
      workspace({ id: "ws-b", slug: "workspace-b" }),
    ]);
    qc.setQueryData(notificationPreferenceKeys.all("ws-a"), {
      system_notifications: "all",
    });
    const showNotification = stubDesktopAPI();

    await handleInboxNew(qc, inboxItem());

    expect(showNotification).toHaveBeenCalledWith({
      slug: "",
      itemId: "item-1",
      issueKey: "issue-1",
      title: "Mentioned you",
      body: "in a comment",
    });
  });

  it("invalidates the ITEM's workspace inbox cache and resolves its slug, not the active workspace's", async () => {
    const qc = createQueryClient();
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [
      workspace({ id: "ws-b", slug: "workspace-b" }),
      workspace(),
    ]);
    qc.setQueryData(notificationPreferenceKeys.all("ws-a"), {
      system_notifications: "all",
    });
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const showNotification = stubDesktopAPI();

    await handleInboxNew(qc, inboxItem());

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: inboxKeys.list("ws-a"),
    });
    expect(showNotification).toHaveBeenCalledWith(
      expect.objectContaining({ slug: "workspace-a" }),
    );
  });

  it("honors the SOURCE workspace's mute preference", async () => {
    const qc = createQueryClient();
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [workspace()]);
    qc.setQueryData(notificationPreferenceKeys.all("ws-a"), {
      system_notifications: "muted",
    });
    const showNotification = stubDesktopAPI();

    await handleInboxNew(qc, inboxItem());

    expect(showNotification).not.toHaveBeenCalled();
  });

  // The tests below exercise the COLD-cache mute path (source preference not
  // yet cached), where the request — not just the query key — must be scoped
  // to the source workspace (#3766 follow-up). They install a fake API so the
  // outgoing call's workspace argument is observable.
  afterEach(() => {
    setApiInstance(undefined as unknown as ApiClient);
  });

  it("fetches the SOURCE workspace's preference using its slug when the cache is cold", async () => {
    const qc = createQueryClient();
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [
      workspace({ id: "ws-b", slug: "workspace-b", name: "Workspace B" }),
      workspace(),
    ]);
    // No cached preference for ws-a → the handler must fetch, and the fetch
    // must target the source workspace's slug, not the active workspace's.
    const getNotificationPreferences = vi
      .fn()
      .mockResolvedValue({ system_notifications: "all" });
    setApiInstance({ getNotificationPreferences } as unknown as ApiClient);
    const showNotification = stubDesktopAPI();

    await handleInboxNew(qc, inboxItem());

    expect(getNotificationPreferences).toHaveBeenCalledWith("workspace-a");
    expect(showNotification).toHaveBeenCalledWith(
      expect.objectContaining({ slug: "workspace-a" }),
    );
  });

  it("suppresses the banner when the SOURCE workspace is muted on a cold cache", async () => {
    const qc = createQueryClient();
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [workspace()]);
    const getNotificationPreferences = vi
      .fn()
      .mockResolvedValue({ system_notifications: "muted" });
    setApiInstance({ getNotificationPreferences } as unknown as ApiClient);
    const showNotification = stubDesktopAPI();

    await handleInboxNew(qc, inboxItem());

    expect(getNotificationPreferences).toHaveBeenCalledWith("workspace-a");
    expect(showNotification).not.toHaveBeenCalled();
  });

  it("never fetches the active workspace's preference when the source slug can't be resolved", async () => {
    const qc = createQueryClient();
    // Item's workspace is absent from the cached list → slug unresolvable.
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [
      workspace({ id: "ws-b", slug: "workspace-b" }),
    ]);
    const getNotificationPreferences = vi
      .fn()
      .mockResolvedValue({ system_notifications: "muted" });
    setApiInstance({ getNotificationPreferences } as unknown as ApiClient);
    const showNotification = stubDesktopAPI();

    await handleInboxNew(qc, inboxItem());

    // Must NOT fall back to the active workspace's preference — that both
    // mis-mutes and pollutes the source workspace's cache key (#3766).
    expect(getNotificationPreferences).not.toHaveBeenCalled();
    expect(showNotification).toHaveBeenCalledWith(
      expect.objectContaining({ slug: "" }),
    );
  });

  // --- Web path: no desktopAPI → the browser Notification API ---
  // Same focus/mute gating as desktop, but the desktop bridge is absent and a
  // granted browser Notification stub is installed on `window`.
  let webBanners: { title: string; options?: NotificationOptions }[] = [];
  class FakeNotification {
    static permission: NotificationPermission = "granted";
    onclick: (() => void) | null = null;
    close = vi.fn();
    constructor(
      public title: string,
      public options?: NotificationOptions,
    ) {
      webBanners.push({ title, options });
    }
  }
  function installBrowserNotification(
    permission: NotificationPermission = "granted",
  ) {
    webBanners = [];
    FakeNotification.permission = permission;
    (globalThis as Record<string, unknown>).window = {
      Notification: FakeNotification,
      focus: vi.fn(),
    };
  }

  afterEach(() => {
    delete (globalThis as Record<string, unknown>).window;
  });

  it("shows a browser banner on web (no desktopAPI) when granted and not muted", async () => {
    const qc = createQueryClient();
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [workspace()]);
    qc.setQueryData(notificationPreferenceKeys.all("ws-a"), {
      system_notifications: "all",
    });
    installBrowserNotification("granted");

    await handleInboxNew(qc, inboxItem());

    expect(webBanners).toHaveLength(1);
    expect(webBanners[0]?.title).toBe("Mentioned you");
  });

  it("shows no browser banner when the SOURCE workspace is muted", async () => {
    const qc = createQueryClient();
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [workspace()]);
    qc.setQueryData(notificationPreferenceKeys.all("ws-a"), {
      system_notifications: "muted",
    });
    installBrowserNotification("granted");

    await handleInboxNew(qc, inboxItem());

    expect(webBanners).toHaveLength(0);
  });

  it("shows no browser banner when permission is not granted", async () => {
    const qc = createQueryClient();
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [workspace()]);
    qc.setQueryData(notificationPreferenceKeys.all("ws-a"), {
      system_notifications: "all",
    });
    installBrowserNotification("default");

    await handleInboxNew(qc, inboxItem());

    expect(webBanners).toHaveLength(0);
  });
});
