type DesktopOS = "macos" | "windows" | "linux" | "unknown";

export interface DesktopAppInfo {
  version: string;
  os: DesktopOS;
}

export interface InboxNotificationPayload {
  slug: string;
  itemId: string;
  issueKey: string;
  title: string;
  body: string;
}

export type InboxOpenPayload = Pick<
  InboxNotificationPayload,
  "slug" | "itemId" | "issueKey"
>;

export interface DaemonCommandResult {
  success: boolean;
  error?: string;
}

export interface UpdaterReleaseInfo {
  version: string;
}

export type UpdateCheckResult =
  | {
      ok: true;
      currentVersion: string;
      latestVersion: string;
      available: boolean;
    }
  | { ok: false; error: string };
