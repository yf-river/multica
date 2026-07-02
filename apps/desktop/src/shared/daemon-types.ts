export type DaemonState =
  | "running"
  | "stopped"
  | "starting"
  | "stopping"
  | "installing_cli"
  | "cli_not_found"
  // The daemon can't start because the server rejected its credentials (the
  // cached PAT expired / was revoked, or the session token is dead). Without
  // this, an auth failure silently sticks at "starting" forever — see #3512.
  | "auth_expired";

export interface DaemonStatus {
  state: DaemonState;
  pid?: number;
  uptime?: string;
  daemonId?: string;
  deviceName?: string;
  agents?: string[];
  workspaceCount?: number;
  /** CLI profile this daemon belongs to. Empty string means the default profile. */
  profile?: string;
  /** Backend URL the daemon connects to. */
  serverUrl?: string;
  /**
   * True when a daemon is running but in an environment the app can't control
   * — its reported OS differs from the desktop host's (e.g. a Linux daemon
   * inside WSL2 behind a Windows desktop, reachable only via localhost
   * forwarding). The app's start/stop CLI acts on the host process namespace,
   * so auto-start/auto-stop can't reach it; the UI disables those toggles
   * instead of silently no-op'ing. Only ever set on a running daemon, so it
   * never disables the toggles for a normally-managed native daemon. See #3916.
   */
  externallyManaged?: boolean;
}

export interface DaemonPrefs {
  autoStart: boolean;
  autoStop: boolean;
}

export const DAEMON_STATE_COLORS: Record<DaemonState, string> = {
  running: "bg-emerald-500",
  stopped: "bg-muted-foreground/40",
  starting: "bg-amber-500 animate-pulse",
  stopping: "bg-amber-500 animate-pulse",
  installing_cli: "bg-sky-500 animate-pulse",
  cli_not_found: "bg-red-500",
  auth_expired: "bg-red-500",
};

export const DAEMON_STATE_LABELS: Record<DaemonState, string> = {
  running: "Running",
  stopped: "Stopped",
  starting: "Starting…",
  stopping: "Stopping…",
  installing_cli: "Setting up…",
  cli_not_found: "Setup Failed",
  auth_expired: "Sign-in required",
};

export function formatUptime(uptime?: string): string {
  if (!uptime) return "";
  const match = uptime.match(/(?:(\d+)h)?(\d+)m/);
  if (!match) return uptime;
  const h = match[1] ? `${match[1]}h ` : "";
  const m = match[2] ? `${match[2]}m` : "";
  return `${h}${m}`.trim() || uptime;
}

/**
 * Whether a raw daemon `/health` `status` value means a live daemon is on the
 * port — either fully "running" (ready) or still "starting" (port bound,
 * preflight in progress). Mirrors the Go `daemonAlive()` in
 * server/cmd/multica/cmd_daemon.go so the Desktop lifecycle agrees with the
 * CLI: a "starting" daemon is already there and must not be spawned over (the
 * CLI rejects that as "already running"). This is liveness, not readiness —
 * version-restart decisions still gate on the stricter "running".
 */
export function daemonStatusAlive(status: string | undefined): boolean {
  return status === "running" || status === "starting";
}
