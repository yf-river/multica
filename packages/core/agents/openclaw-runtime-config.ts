// Mirrors the daemon's OpenClaw runtime_config schema.

export type OpenclawRoutingMode = "local" | "gateway";

export interface OpenclawGatewayPin {
  host?: string;
  port?: number;
  token?: string;
  tls?: boolean;
}

export interface OpenclawRuntimeConfig {
  mode: OpenclawRoutingMode;
  gateway?: OpenclawGatewayPin;
}

// Resubmitting this masked token preserves the stored gateway credential.
export const OPENCLAW_GATEWAY_TOKEN_MASK = "***";

export function parseOpenclawRuntimeConfig(
  raw: unknown,
): OpenclawRuntimeConfig {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return { mode: "local" };
  const root = raw as Record<string, unknown>;
  const mode = root.mode === "gateway" ? "gateway" : "local";
  const out: OpenclawRuntimeConfig = { mode };
  if (mode === "gateway" && root.gateway && typeof root.gateway === "object" && !Array.isArray(root.gateway)) {
    const gw = root.gateway as Record<string, unknown>;
    const pin: OpenclawGatewayPin = {};
    if (typeof gw.host === "string" && gw.host !== "") pin.host = gw.host;
    if (typeof gw.port === "number" && Number.isFinite(gw.port) && gw.port > 0) pin.port = gw.port;
    if (typeof gw.token === "string" && gw.token !== "") pin.token = gw.token;
    if (typeof gw.tls === "boolean") pin.tls = gw.tls;
    if (Object.keys(pin).length > 0) out.gateway = pin;
  }
  return out;
}

export function serializeOpenclawRuntimeConfig(
  cfg: OpenclawRuntimeConfig,
): Record<string, unknown> {
  const out: Record<string, unknown> = { mode: cfg.mode };
  if (cfg.mode === "gateway" && cfg.gateway) {
    const gw: Record<string, unknown> = {};
    if (cfg.gateway.host) gw.host = cfg.gateway.host;
    if (cfg.gateway.port) gw.port = cfg.gateway.port;
    if (cfg.gateway.tls) gw.tls = true;
    if (cfg.gateway.token) {
      gw.token = cfg.gateway.token;
    }
    if (Object.keys(gw).length > 0) out.gateway = gw;
  }
  return out;
}

export function openclawRuntimeConfigEquals(
  a: OpenclawRuntimeConfig,
  b: OpenclawRuntimeConfig,
): boolean {
  if (a.mode !== b.mode) return false;
  if (a.mode === "local") return true;
  const aGw = a.gateway ?? {};
  const bGw = b.gateway ?? {};
  if ((aGw.host ?? "") !== (bGw.host ?? "")) return false;
  if ((aGw.port ?? 0) !== (bGw.port ?? 0)) return false;
  if ((aGw.token ?? "") !== (bGw.token ?? "")) return false;
  if (Boolean(aGw.tls) !== Boolean(bGw.tls)) return false;
  return true;
}
