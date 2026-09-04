import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { CoreProvider } from "@multica/core/platform";
import { useAuthStore } from "@multica/core/auth";
import { useWelcomeStore } from "@multica/core/onboarding";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { useWorkspaceList } from "@multica/core/workspace";
import { api } from "@multica/core/api";
import { useHasOnboarded } from "@multica/core/paths";
import { setCurrentWorkspace } from "@multica/core/platform";
import { ThemeProvider } from "@multica/ui/components/common/theme-provider";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { Toaster } from "@multica/ui/components/ui/sonner";
import { DesktopLoginPage } from "./pages/login";
import { DesktopAuthRecoveryPage } from "./pages/auth-recovery";
import { DesktopShell } from "./components/desktop-layout";
import { UpdateNotification } from "./components/update-notification";
import { IssueWindow } from "./components/issue-window";
import { useTabStore } from "./stores/tab-store";
import { useWindowOverlayStore } from "./stores/window-overlay-store";
import { useOpenSettingsShortcut } from "./hooks/use-open-settings-shortcut";
import { useDaemonIPCBridge } from "./platform/daemon-ipc-bridge";
import { syncDaemonOnLogin } from "./platform/daemon-login-sync";
import { captureEvent } from "@multica/core/analytics";
import { RESOURCES } from "@multica/views/locales";
import { DesktopClientUsageReporter } from "./platform/client-usage-reporter";
import { DiagnosticRouteReporter } from "./platform/diagnostic-route-reporter";
import { flushFreezeBreadcrumb } from "./freeze-flush";
import { DesktopAuthSessionBridge } from "./platform/auth-session-bridge";

/**
 * Cmd/Ctrl+W: close the active tab. When the last real tab is closed
 * (or no tabs/workspace exist — e.g. login page), close the window.
 *
 * Mounted at the App root so every renderer state — including login,
 * loading, onboarding, and runtime-config errors — has a working Cmd+W
 * handler. Without this, states outside the tab shell would swallow the
 * shortcut and do nothing.
 */
function useCmdWCloseTab() {
  useEffect(() => {
    return window.desktopAPI.onCloseActiveTab(() => {
      if (window.desktopAPI.windowContext?.kind === "issue") {
        window.desktopAPI.closeWindow();
        return;
      }
      const store = useTabStore.getState();
      const { activeWorkspaceSlug, byWorkspace } = store;
      if (!activeWorkspaceSlug) {
        // No workspace — nothing to close, dismiss the window.
        window.desktopAPI.closeWindow();
        return;
      }
      const group = byWorkspace[activeWorkspaceSlug];
      if (!group || group.tabs.length <= 1) {
        // Last tab (or no tabs) — close the window.
        window.desktopAPI.closeWindow();
        return;
      }
      // Multiple tabs — close the active one.
      store.closeActiveTab();
    });
  }, []);
}

function IssueWindowContent() {
  const user = useAuthStore((state) => state.user);
  const isLoading = useAuthStore((state) => state.isLoading);
  const authStatus = useAuthStore((state) => state.status);
  const context = window.desktopAPI.windowContext ?? { kind: "main" as const };

  if (context.kind !== "issue") return null;
  if (authStatus === "recovering") return <DesktopAuthRecoveryPage />;
  if (isLoading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <MulticaIcon className="size-6 animate-pulse" />
      </div>
    );
  }

  return user ? <IssueWindow context={context} /> : <DesktopLoginPage />;
}

function AppContent() {
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const authStatus = useAuthStore((s) => s.status);
  const qc = useQueryClient();

  // Deep-link login runs loginWithToken → syncToken → listWorkspaces →
  // setQueryData sequentially. loginWithToken sets user+isLoading=false
  // as soon as getMe resolves, which would cause DesktopShell to mount
  // before the workspace list is hydrated and briefly see `!workspace`.
  // This local flag keeps the loading screen up until the whole chain
  // finishes, so IndexRedirect gets a definitive workspace state on
  // first render.
  const [bootstrapping, setBootstrapping] = useState(false);

  const runtimeConfig = window.desktopAPI.runtimeConfig.ok
    ? window.desktopAPI.runtimeConfig.config
    : null;

  // Tell the main process which backend URL we talk to, so daemon-manager
  // can pick the matching CLI profile (server_url from ~/.multica config).
  useEffect(() => {
    if (!runtimeConfig) return;
    window.daemonAPI.setTargetApiUrl(runtimeConfig.apiUrl);
  }, [runtimeConfig]);

  // Listen for invite IDs delivered via deep link (multica://invite/<id>).
  // We open the overlay regardless of login state — if the user isn't logged
  // in, InvitePage's queries will fail and render the "not found" state,
  // which is acceptable; the expected pre-flight happens in the web app
  // (login + next=/invite/... dance) before the deep link is ever dispatched.
  useEffect(() => {
    return window.desktopAPI.onInviteOpen((invitationId) => {
      useWindowOverlayStore.getState().open({ type: "invite", invitationId });
    });
  }, []);

  // Listen for auth token delivered via deep link (multica://auth/callback?token=...).
  // daemonAPI.syncToken is handled separately by the [user] effect below, which
  // fires whenever a user logs in (deep link, session restore, account switch).
  useEffect(() => {
    return window.desktopAPI.onAuthToken(async (token) => {
      setBootstrapping(true);
      try {
        await useAuthStore.getState().loginWithToken(token);
        // Seed React Query cache with the workspace list so the index-route
        // redirect (routes.tsx `IndexRedirect`) can resolve the initial
        // destination without a second fetch. Workspace side-effects
        // (setCurrentWorkspace, persist namespace) are synced later by
        // WorkspaceRouteLayout when the URL resolves.
        const wsList = await api.listWorkspaces();
        qc.setQueryData(workspaceKeys.list(), wsList);
      } catch {
        // Token invalid or expired — user stays on login page
      } finally {
        setBootstrapping(false);
      }
    });
  }, [qc]);

  // Sync token and start the daemon whenever the user logs in. The ordering
  // inside syncDaemonOnLogin is load-bearing — see that module.
  useEffect(() => {
    if (!user || !runtimeConfig) return;
    const token = localStorage.getItem("multica_token");
    if (!token) return;
    const userId = user.id;
    (async () => {
      try {
        await syncDaemonOnLogin(
          window.daemonAPI,
          runtimeConfig.apiUrl,
          token,
          userId,
        );
      } catch (err) {
        console.error("Failed to sync daemon on login", err);
      }
    })();
  }, [user, runtimeConfig]);

  // When a user who started the session with zero workspaces creates their
  // first one, restart the daemon so it picks up the new workspace
  // immediately (otherwise workspaceSyncLoop's next 30s tick would be the
  // earliest pickup point). Specifically scoped to "started empty" because
  // account switches (user A logout → user B login) should not trigger a
  // daemon restart here — daemon-manager already restarts on user change
  // via syncToken.
  const {
    workspaces,
    ready: workspaceListReady,
    unavailable: workspaceListUnavailable,
    isFetching: workspaceListRetrying,
    refetch: retryWorkspaceList,
  } = useWorkspaceList({
    enabled: !!user,
  });
  const wsCount = workspaces.length;
  const hasOnboarded = useHasOnboarded();

  // Bridge local daemon IPC status into the runtimes cache so this user's
  // own daemon flips to offline/online sub-second instead of waiting on the
  // server's 75s sweeper. Resolves wsId from the active tab so workspace
  // switches automatically rebind the subscription.
  const activeWorkspaceSlug = useTabStore((s) => s.activeWorkspaceSlug);
  const activeWsId = activeWorkspaceSlug
    ? workspaces.find((w) => w.slug === activeWorkspaceSlug)?.id
    : undefined;
  useDaemonIPCBridge(activeWsId);

  // Pre-workspace overlay routing for desktop. Mirrors the web layout
  // hard gate via overlays (desktop has no URL bar, so we open the
  // onboarding overlay instead of router.replace):
  //   onboarded + has workspace      → no overlay, dashboard
  //   un-onboarded (any wsCount):
  //     pending invites on email     → /invitations overlay
  //     no invites                   → /onboarding overlay
  //   onboarded + no workspace       → /workspaces/new overlay
  //
  // V3 invariant: `onboarded_at != null` is the only path into the
  // dashboard. CreateWorkspace does not mark onboarded; only Step 3's
  // CompleteOnboarding (and AcceptInvitation) flip the flag. A user who
  // somehow has a workspace but no onboarded mark must be sent back to
  // /onboarding — we also clear the active workspace so the dashboard
  // doesn't render under the overlay with stale workspace context.
  useEffect(() => {
    if (!user || !workspaceListReady) return undefined;
    const { overlay, open } = useWindowOverlayStore.getState();
    if (overlay) return undefined;
    if (hasOnboarded && wsCount > 0) return undefined;
    if (!hasOnboarded) {
      // Stale workspace context (if any) would leak X-Workspace-Slug
      // headers into onboarding-time API calls. Clear it before opening
      // the overlay.
      setCurrentWorkspace(null, null);
      // Look up pending invitations by email. Network blip is non-fatal —
      // fall through to onboarding so the user isn't stuck on a blank
      // window. The sidebar's pending-invitations dropdown will surface
      // missed invites later once they're onboarded.
      let cancelled = false;
      void api
        .listMyInvitations()
        .then((invites) => {
          if (cancelled) return;
          const { overlay: latestOverlay, open: latestOpen } =
            useWindowOverlayStore.getState();
          if (latestOverlay) return;
          if (invites.length > 0) {
            qc.setQueryData(workspaceKeys.myInvitations(), invites);
            latestOpen({ type: "invitations" });
          } else {
            latestOpen({ type: "onboarding" });
          }
        })
        .catch(() => {
          if (cancelled) return;
          const { overlay: latestOverlay, open: latestOpen } =
            useWindowOverlayStore.getState();
          if (latestOverlay) return;
          latestOpen({ type: "onboarding" });
        });
      return () => {
        cancelled = true;
      };
    }
    open({ type: "new-workspace" });
    return undefined;
  }, [user, workspaceListReady, wsCount, workspaces, hasOnboarded, qc]);


  // Validate persisted tab state against the current user's workspace list,
  // and pick an active workspace if none is set. Runs in useLayoutEffect
  // (synchronously after render, before paint) rather than the render
  // phase — the original render-phase pattern triggered React's
  // "Cannot update a component while rendering a different component"
  // warning because `switchWorkspace` is a Zustand setState that the
  // TabBar is subscribed to. useLayoutEffect flushes both renders before
  // the user sees anything, so there's no visible flicker.
  //
  // Gate on authoritative data: pending and initial errors expose no data,
  // while a failed background refetch retains the last successful list.
  useLayoutEffect(() => {
    if (!workspaceListReady) return;
    const validSlugs = new Set(workspaces.map((w) => w.slug));
    useTabStore.getState().validateWorkspaceSlugs(validSlugs);
    const { activeWorkspaceSlug, switchWorkspace } = useTabStore.getState();
    if (!activeWorkspaceSlug && workspaces.length > 0) {
      switchWorkspace(workspaces[0].slug);
    }
  }, [workspaces, workspaceListReady]);

  // null = undecided (pre-login or list hasn't settled yet)
  // true  = session started with zero workspaces; next transition to >=1 triggers restart
  // false = session started with >=1 workspace, OR we've already restarted; skip
  const sessionStartedEmptyRef = useRef<boolean | null>(null);
  useEffect(() => {
    if (!user) {
      sessionStartedEmptyRef.current = null;
      return;
    }
    if (!workspaceListReady) return;
    if (sessionStartedEmptyRef.current === null) {
      sessionStartedEmptyRef.current = wsCount === 0;
      return;
    }
    if (sessionStartedEmptyRef.current && wsCount >= 1) {
      void window.daemonAPI.restart();
      sessionStartedEmptyRef.current = false;
    }
  }, [user, workspaceListReady, wsCount]);

  if (authStatus === "recovering") {
    return <DesktopAuthRecoveryPage />;
  }
  if (isLoading || bootstrapping) {
    return (
      <div className="flex h-screen items-center justify-center">
        <MulticaIcon className="size-6 animate-pulse" />
      </div>
    );
  }

  if (workspaceListUnavailable) {
    return (
      <DesktopAuthRecoveryPage
        isRetrying={workspaceListRetrying}
        onRetry={() => {
          void retryWorkspaceList();
        }}
      />
    );
  }

  return user ? <DesktopShell /> : <DesktopLoginPage />;
}

function BlockingRuntimeConfigError({ message }: { message: string }) {
  return (
    <div className="flex h-screen items-center justify-center bg-background p-8 text-foreground">
      <div className="max-w-xl rounded-lg border bg-card p-6 shadow-sm">
        <h1 className="text-title font-semibold">桌面端配置错误</h1>
        <p className="mt-3 text-body text-muted-foreground">
          Multica 桌面端无法读取 <code>~/.multica/desktop.json</code>。请修复或删除该文件后重启应用。
        </p>
        <pre className="mt-4 whitespace-pre-wrap rounded-md bg-muted p-3 text-caption text-muted-foreground">
          {message}
        </pre>
      </div>
    </div>
  );
}

// On logout, wipe desktop-only in-memory state and stop the daemon so that
// a subsequent login as a different user never inherits the previous user's
// tabs, overlay, or credentials. Zustand persist only writes to localStorage;
// useLogout clears the storage key, but the live stores stay populated until
// we explicitly reset them here.
async function handleDaemonLogout() {
  // Report synchronously before async daemon cleanup so a rapidly closed main
  // window cannot leave authenticated issue renderers behind.
  window.desktopAPI.reportAuthSession?.(null);
  useTabStore.getState().reset();
  useWindowOverlayStore.getState().close();
  // Drop any post-onboarding welcome signal so user B logging in next
  // doesn't inherit user A's pending modal state.
  useWelcomeStore.getState().reset();
  try {
    await window.daemonAPI.clearToken();
  } catch {
    // Best-effort — clearing is followed by stop which also hardens state.
  }
  try {
    await window.daemonAPI.stop();
  } catch {
    // Daemon may already be stopped.
  }
}

export default function App() {
  const { version, os } = window.desktopAPI.appInfo;
  const runtimeConfigResult = window.desktopAPI.runtimeConfig;
  // The fallback keeps renderer HMR safe while a main/preload rebuild is
  // restarting Electron; packaged builds always expose windowContext.
  const windowContext =
    window.desktopAPI.windowContext ?? { kind: "main" as const };
  useCmdWCloseTab();
  // Mounted at the App root for the same reason as Cmd+W: the chord has to
  // work in every renderer state, not only inside the tab shell.
  useOpenSettingsShortcut();

  // Flush a freeze/crash breadcrumb the main process parked from a previous
  // session. A true hang or process death can't report itself when it happens
  // (the renderer is blocked or gone), so the main process persists it and we
  // emit it here on the next boot. The in-thread, recoverable freeze tier is
  // handled separately by the shared watchdog in CoreProvider.
  useEffect(
    () =>
      flushFreezeBreadcrumb({
        getLastFreeze: () => window.desktopAPI.getLastFreeze(),
        ackFreeze: (ts) => window.desktopAPI.ackFreeze(ts),
        capture: captureEvent,
      }),
    [],
  );

  // Stable identity reference so downstream effects (WS reconnect) don't
  // tear down on every parent render.
  const identity = useMemo(
    () => ({ platform: "desktop", version, os }),
    [version, os],
  );
  const locale = "zh-Hans" as const;
  const resources = useMemo(() => ({ "zh-Hans": RESOURCES["zh-Hans"] }), []);

  useEffect(() => {
    document.documentElement.lang = "zh-CN";
  }, []);

  return (
    <ThemeProvider>
      {runtimeConfigResult.ok ? (
        <CoreProvider
          apiBaseUrl={runtimeConfigResult.config.apiUrl}
          wsUrl={runtimeConfigResult.config.wsUrl}
          onLogout={
            windowContext.kind === "main" ? handleDaemonLogout : undefined
          }
          identity={identity}
          locale={locale}
          resources={resources}
        >
          <DesktopAuthSessionBridge />
          {windowContext.kind === "main" && <DiagnosticRouteReporter />}
          {windowContext.kind === "main" && (
            <DesktopClientUsageReporter
              apiUrl={runtimeConfigResult.config.apiUrl}
            />
          )}
          {windowContext.kind === "issue" ? (
            <IssueWindowContent />
          ) : (
            <AppContent />
          )}
        </CoreProvider>
      ) : (
        <BlockingRuntimeConfigError message={runtimeConfigResult.error.message} />
      )}
      <Toaster />
      {windowContext.kind === "main" && <UpdateNotification />}
    </ThemeProvider>
  );
}
