export type RendererRecoveryWindow = {
  isDestroyed: () => boolean;
  on: (event: "unresponsive" | "responsive", handler: () => void) => unknown;
  webContents: {
    on: (event: string, handler: (...args: any[]) => void) => unknown;
    reload: () => void;
  };
};

type ReloadPromptPayload = {
  kind: "render-process-gone" | "preload-error" | "unresponsive";
  context: Record<string, unknown>;
};

type ReloadPromptResult = "reload" | "dismiss";

type RendererRecoveryOptions = {
  isDev: boolean;
  showReloadPrompt: (payload: ReloadPromptPayload) => Promise<ReloadPromptResult>;
  getDiagnosticContext?: () => Record<string, unknown>;
  /**
   * Persist a freeze/crash breadcrumb to disk. The renderer can't report a
   * true hang or process death itself (blocked / gone), so the main process
   * writes it here and the next renderer boot flushes it to telemetry. Omit
   * in dev to keep field telemetry clean.
   */
  persistBreadcrumb?: (payload: ReloadPromptPayload) => void;
  /**
   * Delete a previously-persisted unresponsive breadcrumb. Called when the
   * renderer recovers (`responsive` after `unresponsive`): the window came
   * back, so the in-thread watchdog reports the freeze and the breadcrumb
   * would only double-count it. Crash breadcrumbs are never cleared — a dead
   * process never recovers.
   */
  clearBreadcrumb?: () => void;
  log?: (tag: string, ...args: unknown[]) => void;
  unresponsivePromptDelayMs?: number;
};

export function installRendererRecoveryHandlers(
  window: RendererRecoveryWindow,
  {
    isDev,
    showReloadPrompt,
    getDiagnosticContext,
    persistBreadcrumb,
    clearBreadcrumb,
    log = defaultDevLog,
    unresponsivePromptDelayMs = 1500,
  }: RendererRecoveryOptions,
) {
  let unresponsivePromptTimer: ReturnType<typeof setTimeout> | null = null;
  // True once a breadcrumb has been written for the current hang. A later
  // `responsive` clears it; only a hang that never returns survives to report.
  let unresponsiveBreadcrumbWritten = false;
  const mergeDiagnosticContext = (context: Record<string, unknown>) => ({
    ...readDiagnosticContext(getDiagnosticContext),
    ...context,
  });
  const maybePromptReload = (payload: ReloadPromptPayload) => {
    if (isDev) return;
    void showReloadPrompt(payload).then((result) => {
      if (result === "reload" && !window.isDestroyed()) {
        window.webContents.reload();
      }
    });
  };

  window.webContents.on("render-process-gone", (_event, details) => {
    if (isDev) log("process-gone", JSON.stringify(details));
    if (!isRecoverableRendererExit(details)) return;
    const payload: ReloadPromptPayload = {
      kind: "render-process-gone",
      context: mergeDiagnosticContext({ details }),
    };
    persistBreadcrumb?.(payload);
    maybePromptReload(payload);
  });

  // preload-error intentionally does NOT persist a breadcrumb: it's a startup
  // failure of the preload script itself, and the breadcrumb-flush path depends
  // on that same preload exposing `getLastFreeze` — if preload is broken, the
  // next boot couldn't read it back anyway. We only prompt for reload here.
  window.webContents.on("preload-error", (_event, preloadPath, error) => {
    if (isDev) log("preload-error", `path=${preloadPath} err=${formatError(error)}`);
    maybePromptReload({
      kind: "preload-error",
      context: mergeDiagnosticContext({ preloadPath, error: formatError(error) }),
    });
  });

  window.on("unresponsive", () => {
    if (isDev || unresponsivePromptTimer) return;
    unresponsivePromptTimer = setTimeout(() => {
      unresponsivePromptTimer = null;
      const payload: ReloadPromptPayload = {
        kind: "unresponsive",
        context: mergeDiagnosticContext({}),
      };
      persistBreadcrumb?.(payload);
      unresponsiveBreadcrumbWritten = true;
      maybePromptReload(payload);
    }, unresponsivePromptDelayMs);
  });

  window.on("responsive", () => {
    if (unresponsivePromptTimer) {
      clearTimeout(unresponsivePromptTimer);
      unresponsivePromptTimer = null;
    }
    // The window came back: drop any breadcrumb written during this hang so it
    // isn't re-reported (and mislabeled `recovered: false`) on next boot.
    if (unresponsiveBreadcrumbWritten) {
      clearBreadcrumb?.();
      unresponsiveBreadcrumbWritten = false;
    }
  });
}

export function createElectronReloadPrompt(
  showMessageBox: (options: {
    type: "warning";
    buttons: string[];
    defaultId: number;
    cancelId: number;
    title: string;
    message: string;
    detail: string;
  }) => Promise<{ response: number }>,
) {
  return async (payload: ReloadPromptPayload): Promise<ReloadPromptResult> => {
    const result = await showMessageBox({
      type: "warning",
      buttons: ["重新加载", "忽略"],
      defaultId: 0,
      cancelId: 1,
      title: "Multica 需要重新加载",
      message: rendererRecoveryMessage(payload.kind),
      detail: rendererRecoveryDetail(payload),
    });
    return result.response === 0 ? "reload" : "dismiss";
  };
}

function isRecoverableRendererExit(details: unknown) {
  if (!details || typeof details !== "object") return false;
  const reason = (details as { reason?: unknown }).reason;
  return (
    reason === "crashed" ||
    reason === "oom" ||
    reason === "abnormal-exit" ||
    reason === "launch-failed" ||
    reason === "integrity-failure"
  );
}

function rendererRecoveryMessage(kind: ReloadPromptPayload["kind"]) {
  switch (kind) {
    case "render-process-gone":
      return "桌面窗口意外停止。";
    case "preload-error":
      return "桌面窗口未能完成启动。";
    case "unresponsive":
      return "桌面窗口已经卡住数秒。";
  }
}

function rendererRecoveryDetail(payload: ReloadPromptPayload) {
  const guidance = [
    "点击重新加载以刷新此窗口并继续使用 Multica。",
    "如果问题持续出现，请告诉我们此消息出现前你正在做什么，以及重新加载是否恢复了窗口。",
  ];

  if (payload.kind === "unresponsive") {
    guidance.push(
      "在 macOS 上反馈时，附上 Multica Helper (Renderer) 进程的活动监视器采样，有助于定位卡顿原因。",
    );
  }

  return [
    ...guidance,
    "",
    "诊断详情:",
    `kind: ${payload.kind}`,
    `context: ${JSON.stringify(payload.context)}`,
  ].join("\n");
}

function defaultDevLog(tag: string, ...args: unknown[]) {
  process.stderr.write(`[renderer ${tag}] ${args.map(String).join(" ")}\n`);
}

function readDiagnosticContext(
  getDiagnosticContext: (() => Record<string, unknown>) | undefined,
) {
  if (!getDiagnosticContext) return {};
  try {
    return getDiagnosticContext();
  } catch {
    return {};
  }
}

function formatError(error: unknown) {
  return error instanceof Error ? (error.stack ?? error.message) : String(error);
}
