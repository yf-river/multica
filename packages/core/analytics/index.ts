// Frontend analytics glue. Thin wrapper over posthog-js.
//
// The source-of-truth event catalog is `docs/analytics.md`. This module keeps
// only product analytics for authenticated team usage; marketing signup
// attribution is intentionally disabled in the internal-only account model.
//
// Configuration comes from the backend's `/api/config` response (populated
// from POSTHOG_API_KEY on the server), NOT from NEXT_PUBLIC_* envs. That
// keeps self-hosted Docker images from leaking our project key — their
// backend returns an empty key and this module stays inert.

import posthog from "posthog-js";
import { redactExceptionProperties } from "./redact-exception";
import { shouldDropException } from "./exception-dedupe";
import { isBenignException } from "./benign-exceptions";

export const EVENT_SCHEMA_VERSION = 2;

let initialized = false;
// auth-initializer fetches /api/config and /api/me in parallel — on a
// slow-config path, identify() can fire before initAnalytics(). Buffer the
// most recent pending identify (only one matters, since it's per-session)
// and flush it inside initAnalytics.
let pendingIdentify: { userId: string; props?: Record<string, unknown> } | null = null;
let currentUserId: string | null = null;
let analyticsEnvironment = "dev";
// Likewise pageviews: the Next.js router can fire one before the config fetch
// resolves, so keep the latest pending pageview and flush it once analytics is
// ready.
let pendingPageview: string | undefined | null = null;
// Last $pageview path actually emitted (already section-normalized). Used to
// collapse consecutive views of the same section so navigating between
// resources under one section doesn't fire a billed event per resource. See
// capturePageview / normalizePageviewPath. Cleared on reset so a fresh
// session re-emits its first pageview.
let lastCapturedPath: string | null = null;
// Frontend-emitted events (captureEvent) and person-property updates
// (setPersonProperties) can also arrive before init — same config-race as
// identify/pageview. We replay them in order once init succeeds. These
// only ever carry user-triggered signals on identified users, so the
// buffer stays small (~one step-transition worth).
type PendingOp =
  | { kind: "event"; name: string; props?: Record<string, unknown> }
  | { kind: "set"; props: Record<string, unknown> }
  | { kind: "exception"; error: unknown; props?: Record<string, unknown> };
const pendingOps: PendingOp[] = [];
// Cached super-properties so resetAnalytics() can re-register them after
// posthog.reset() wipes the persisted set. Without this, logout / account
// switch silently drops client_type + app_version from every subsequent
// event until a full reload.
let superProperties: Record<string, unknown> = {};

export {
  captureDownloadIntent,
  captureDownloadPageViewed,
  captureDownloadInitiated,
  type DownloadIntentSource,
  type DownloadDetectPayload,
  type DownloadInitiatedPayload,
} from "./download";

export {
  captureFeedbackOpened,
  type FeedbackOpenedSource,
} from "./feedback";

export interface AnalyticsConfig {
  key: string;
  host: string;
  /**
   * Client app version — attached to every event as an `app_version`
   * super-property. Web injects the build-time tag / sha; desktop reads from
   * the Electron API. Optional because local dev may not have a version
   * available.
   */
  appVersion?: string;
  environment?: string;
}

export type ClientType = "desktop" | "web";

/**
 * Classify the current runtime as desktop (Electron renderer) or web. Used as
 * a super-property so every event can be split by client without relying on
 * PostHog's `$lib`, which reports "web" in both the Next.js app and the
 * Electron renderer (both Chromium).
 *
 * Signals we trust:
 *   - `window.electron` is exposed by the preload script in every renderer.
 *   - `navigator.userAgent` contains "Electron" as a fallback.
 */
export function detectClientType(): ClientType {
  if (typeof window === "undefined") return "web";
  const w = window as unknown as { electron?: unknown; desktopAPI?: unknown };
  if (w.electron || w.desktopAPI) return "desktop";
  if (typeof navigator !== "undefined" && /Electron/i.test(navigator.userAgent)) {
    return "desktop";
  }
  return "web";
}

/**
 * Initialize posthog-js if a key is present. Safe to call multiple times;
 * subsequent calls with the same config are no-ops.
 *
 * Returns `true` when analytics is actually running; `false` when disabled
 * (no key, SSR, or already initialized with a conflicting key — which we
 * treat as "use the existing instance").
 */
export function initAnalytics(config: AnalyticsConfig | null | undefined): boolean {
  if (typeof window === "undefined") return false;
  if (!config?.key) return false;
  if (initialized) return true;

  posthog.init(config.key, {
    api_host: config.host || "https://us.i.posthog.com",
    // person_profiles=identified_only keeps anonymous drive-by traffic off
    // the billed events until they actually identify, which aligns with how
    // our funnel is set up: signup is the first real funnel step.
    person_profiles: "identified_only",
    // Turn off every on-by-default auto-capture surface. Our funnel is
    // narrow and explicit (the events in docs/analytics.md + a manual
    // $pageview). Autocapture floods the Activity view with anonymous
    // "clicked button" / "clicked link" noise, burns the billed event
    // budget, and risks capturing user-typed content in input values.
    // Turn things back on deliberately if we ever want them.
    capture_pageview: false,
    autocapture: false,
    capture_heatmaps: false,
    capture_dead_clicks: false,
    // Exception autocapture IS on: posthog-js attaches window.onerror +
    // unhandledrejection handlers and sends `$exception` events with the
    // error's stack. Unlike the click/heatmap autocapture above, this is
    // explicit failure signal (not behavioral noise) and is the one PostHog
    // surface that natively handles thrown JS errors — see the failure-tier
    // split in packages/core/diagnostics. (Production builds are minified;
    // upload source maps to PostHog to de-minify the stacks.)
    //
    // Error messages can interpolate user input (a validation error with the
    // typed value, a URL with a token), so `before_send` scrubs the message
    // and `$exception_list[].value` before the event leaves the client. Stack
    // frames (code locations) are kept. See redact-exception.ts.
    //
    // After scrubbing, a session-level fuse drops repeats of the same error so
    // a render loop or a polling fetch that keeps throwing can't emit 100+
    // identical `$exception` events per session (MUL-3331). The fingerprint is
    // built only from the already-redacted fields, so no PII reaches storage.
    // Order matters: redact first, then fingerprint the redacted shape.
    capture_exceptions: true,
    before_send: (event) => {
      if (event && event.event === "$exception") {
        // Drop known-benign browser noise (e.g. ResizeObserver loop) entirely
        // — checked on the raw message before redaction. These dominate the
        // stream and carry no signal, so they skip both redaction and the
        // dedupe fuse. See benign-exceptions.ts.
        if (isBenignException(event.properties)) return null;
        redactExceptionProperties(event.properties);
        if (shouldDropException(event.properties)) return null;
      }
      return event;
    },
    disable_session_recording: true,
    disable_surveys: true,
  });
  analyticsEnvironment = normalizeEnvironment(config.environment);
  // Register super-properties — attached to every event emitted from this
  // client. `client_type` is the canonical split between desktop and web
  // (PostHog's own `$lib` reports "web" for both because Electron renderers
  // are Chromium). `app_version` is optional so self-hosted or local dev
  // builds without a version don't pollute the property.
  // We cache the set so resetAnalytics() can re-apply it after
  // posthog.reset() — reset() clears persisted super-properties otherwise.
  superProperties = {
    client_type: detectClientType(),
    event_schema_version: EVENT_SCHEMA_VERSION,
    environment: analyticsEnvironment,
    is_demo: false,
  };
  if (config.appVersion) superProperties.app_version = config.appVersion;
  posthog.register(superProperties);
  initialized = true;

  // Flush any identify() that arrived before init resolved.
  if (pendingIdentify) {
    currentUserId = pendingIdentify.userId;
    posthog.identify(pendingIdentify.userId, pendingIdentify.props);
    pendingIdentify = null;
  }
  // And any first pageview we captured while config was loading.
  if (pendingPageview !== null) {
    posthog.capture("$pageview", pendingPageview ? { $current_url: pendingPageview } : undefined);
    lastCapturedPath = pendingPageview ?? null;
    pendingPageview = null;
  }
  // Replay buffered events / person-property updates in their original
  // order — funnel correctness depends on sequence (e.g. a user submits
  // the questionnaire and then finishes onboarding within the same
  // config-race window).
  while (pendingOps.length > 0) {
    const op = pendingOps.shift()!;
    if (op.kind === "event") {
      posthog.capture(op.name, withClientEventProperties(op.props));
    } else if (op.kind === "exception") {
      posthog.captureException(op.error, withClientEventProperties(op.props));
    } else {
      capturePersonSet(op.props);
    }
  }
  return true;
}

/**
 * Merge the current anonymous session into the logged-in person. Must be
 * called exactly once per auth transition (login / session-resume). Pulling
 * attribution properties into person_properties on identify is how we keep
 * UTM / referrer on the user profile without re-emitting them per event.
 *
 * Calls before initAnalytics() are buffered — auth-initializer fetches
 * config and user in parallel, so identify can arrive first.
 */
export function identify(userId: string, userProperties?: Record<string, unknown>): void {
  currentUserId = userId;
  if (!initialized) {
    pendingIdentify = { userId, props: userProperties };
    return;
  }
  posthog.identify(userId, userProperties);
}

/**
 * Clear the client-side identity on logout so the next login merges cleanly
 * and doesn't bleed the previous user's events into a new session.
 */
export function resetAnalytics(): void {
  currentUserId = null;
  pendingIdentify = null;
  pendingPageview = null;
  lastCapturedPath = null;
  pendingOps.length = 0;
  if (!initialized) return;
  posthog.reset();
  // reset() wipes persisted super-properties too, so re-register the ones
  // set at init time. Otherwise every event after logout / account-switch
  // would be missing client_type + app_version until a full reload.
  if (Object.keys(superProperties).length > 0) {
    posthog.register(superProperties);
  }
}

/**
 * Capture a frontend-emitted event. Most funnel events fire server-side
 * (see `server/internal/analytics`); this wrapper is reserved for the
 * handful of signals the backend can't see — primarily the Step 3
 * platform-fork choice on web, where the user's click never round-trips
 * to a handler.
 *
 * Calls before initAnalytics() buffer in order so a late-arriving config
 * doesn't silently swallow a step transition.
 */
export function captureEvent(
  name: string,
  props?: Record<string, unknown>,
): void {
  if (!initialized) {
    pendingOps.push({ kind: "event", name, props });
    return;
  }
  posthog.capture(name, withClientEventProperties(props));
}

/**
 * Report a caught exception that never reached `window.onerror` — a React
 * render-phase error swallowed by an error boundary. Global uncaught errors
 * and unhandled rejections are already captured automatically by posthog-js
 * (`capture_exceptions: true`); this wrapper is for the boundary case those
 * handlers can't see.
 *
 * Currently called by the web route-level `global-error`. Section-level
 * `@multica/ui` ErrorBoundary can opt in by passing `onError={captureException}`
 * at its call sites; it is not wired app-wide (those failures already degrade
 * gracefully with fallback UI).
 *
 * Calls before initAnalytics() buffer in order, same as captureEvent.
 */
export function captureException(
  error: unknown,
  props?: Record<string, unknown>,
): void {
  if (!initialized) {
    pendingOps.push({ kind: "exception", error, props });
    return;
  }
  posthog.captureException(error, withClientEventProperties(props));
}

/**
 * Set (overwrite) person properties on the currently identified user.
 * Mirrors the backend's `Event.Set` path — keep these aligned so the
 * same cohort signals (role, use_case, platform_preference) are
 * queryable regardless of which side emitted last. Use for mutable
 * signals; use `identify(userId, { $set_once: {...} })` style for
 * attribution fields that must never be overwritten.
 */
export function setPersonProperties(props: Record<string, unknown>): void {
  if (!initialized) {
    pendingOps.push({ kind: "set", props });
    return;
  }
  capturePersonSet(props);
}

// The public wire-level contract for `$set` is a no-op event carrying a
// `$set` property. Wrapping it here (rather than calling
// `posthog.setPersonProperties` directly) keeps us version-independent —
// older posthog-js builds expose the same protocol under `posthog.people.set`,
// and the capture form works uniformly.
function capturePersonSet(props: Record<string, unknown>): void {
  posthog.capture("$set", { $set: props });
}

function withClientEventProperties(
  props?: Record<string, unknown>,
): Record<string, unknown> {
  const next: Record<string, unknown> = { ...(props ?? {}) };
  if (currentUserId && next.user_id === undefined) {
    next.user_id = currentUserId;
  }
  if (next.event_schema_version === undefined) {
    next.event_schema_version = EVENT_SCHEMA_VERSION;
  }
  if (next.environment === undefined) {
    next.environment = analyticsEnvironment;
  }
  if (next.is_demo === undefined) {
    next.is_demo = false;
  }
  return next;
}

function normalizeEnvironment(value: string | undefined): string {
  switch ((value || "").trim().toLowerCase()) {
    case "production":
    case "prod":
      return "production";
    case "staging":
    case "stage":
      return "staging";
    case "development":
    case "dev":
    case "test":
    case "local":
      return "dev";
    default:
      return "dev";
  }
}

// A UUID or an issue key (e.g. MUL-123) appearing as a path segment
// identifies a single resource. Resource-level granularity carries no
// aggregate funnel signal and explodes $pageview volume — every distinct
// issue / agent / project navigated to would fire its own billed event.
const UUID_SEGMENT = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const ISSUE_KEY_SEGMENT = /^[A-Z][A-Z0-9]*-\d+$/;

/**
 * Normalize a raw path to its section route for $pageview reporting:
 * `/acme/issues/8d5c…` and `/acme/issues/MUL-12` both collapse to
 * `/acme/issues`. We strip the query string / hash (volatile filter / sort /
 * search state, and occasionally OAuth `code` / `state`) and drop any
 * resource-id segment after the first. The leading segment — the workspace
 * slug or a top-level route word like `login` — is always kept, so a slug
 * that happens to look like an id (`team-1`) is never dropped.
 *
 * Exported for unit testing; callers should go through capturePageview.
 */
export function normalizePageviewPath(path?: string): string | undefined {
  if (!path) return path ?? undefined;
  const clean = path.split(/[?#]/)[0] ?? "";
  const segments = clean.split("/").filter((s) => s.length > 0);
  const kept = segments.filter(
    (seg, i) => i === 0 || !(UUID_SEGMENT.test(seg) || ISSUE_KEY_SEGMENT.test(seg)),
  );
  return "/" + kept.join("/");
}

/**
 * Capture a page view. Call once per client-side navigation. We disable
 * posthog's automatic pageview tracking in init() so this module owns the
 * event shape.
 *
 * The path is normalized to its section route (see normalizePageviewPath) and
 * consecutive views of the same section are collapsed — both keep PostHog at
 * section granularity instead of paying for a billed event per resource and
 * per query-string change. Callers can therefore pass the raw path freely.
 *
 * Calls before initAnalytics() buffer the most-recent path so the first
 * pageview isn't dropped on slow /api/config fetches. Subsequent pre-init
 * pageviews overwrite the buffer; after init flushes, every navigation
 * captures synchronously as expected.
 */
export function capturePageview(path?: string): void {
  const normalized = normalizePageviewPath(path);
  if (!initialized) {
    pendingPageview = normalized ?? "";
    return;
  }
  if (normalized && normalized === lastCapturedPath) return;
  lastCapturedPath = normalized ?? null;
  posthog.capture("$pageview", normalized ? { $current_url: normalized } : undefined);
}
