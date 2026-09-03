/** A Composio toolkit as surfaced by GET /api/integrations/composio/toolkits.
 *
 * Wire shape mirrors `ComposioToolkitResponse` in
 * `server/internal/handler/integrations_composio.go`. The backend only
 * returns toolkits with an enabled project auth configuration. */
export interface ComposioToolkit {
  slug: string;
  name: string;
  logo?: string;
  category?: string;
}

/** A user's Composio connected account, as returned by
 * GET /api/integrations/composio/connections. Mirrors
 * `ComposioConnectionResponse` server-side. */
export interface ComposioConnection {
  id: string;
  toolkit_slug: string;
  /** Connection lifecycle state. `expired` surfaces a Reconnect affordance in
   * the UI; the backend only starts emitting it once Stage 4 webhook handling
   * lands (MUL-3719), but the client renders the branch ahead of that. */
  status: "active" | "expired" | "revoked" | string;
  connected_at: string;
  last_used_at?: string | null;
}

/** Response of POST /api/integrations/composio/connect/init — the hosted
 * Composio Connect Link the browser is redirected to. */
export interface ComposioConnectInitResponse {
  redirect_url: string;
}
