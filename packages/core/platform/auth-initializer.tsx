// 本文件实现 AuthInitializer：应用启动时负责拉取运行配置、恢复会话（带限速
// 重试），并把认证状态发布进 auth store。

"use client";

import { useCallback, useEffect, useRef, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { getApi } from "../api";
import { ApiError } from "../api/client";
import { useAuthStore } from "../auth";
import {
  captureSignupSource,
  identify as identifyAnalytics,
  initAnalytics,
  resetAnalytics,
} from "../analytics";
import { configStore } from "../config";
import { workspaceListOptions } from "../workspace/queries";
import { createLogger } from "../logger";
import { defaultStorage } from "./storage";
import { setCurrentWorkspace } from "./workspace-storage";
import type { ClientIdentity } from "./types";
import type { StorageAdapter } from "../types/storage";
import type { User } from "../types";

const logger = createLogger("auth");
const RECOVERY_RETRY_DELAYS_MS = [
  1_000,
  2_000,
  4_000,
  8_000,
  16_000,
  30_000,
] as const;

/**
 * AuthInitializer：挂载时负责
 *  1. 给 signup 来源打时间戳；
 *  2. 拉取运行配置（失败则限速重试、联网后立即重试）；
 *  3. 恢复会话（token 或 cookie 模式），并把结果发布进 auth store。
 */
export function AuthInitializer({
  children,
  onLogin,
  onLogout,
  storage = defaultStorage,
  cookieAuth,
  identity,
  environmentId = "",
}: {
  children: ReactNode;
  onLogin?: () => void;
  onLogout?: () => void;
  storage?: StorageAdapter;
  cookieAuth?: boolean;
  identity?: ClientIdentity;
  environmentId?: string;
}) {
  const qc = useQueryClient();
  const retryGeneration = useAuthStore((state) => state.retryGeneration);
  const configLoadedRef = useRef(false);
  const configRequestRef = useRef<Promise<boolean> | null>(null);
  const authRecoveryPendingRef = useRef(false);
  const retryConfigRef = useRef<() => void>(() => {});

  /** 拉取 /api/config 并把结果写进 config store（幂等、去重并发）。 */
  const loadConfig = useCallback((): Promise<boolean> => {
    if (configLoadedRef.current) return Promise.resolve(true);
    if (configRequestRef.current) return configRequestRef.current;

    const request = getApi()
      .getConfig()
      .then((cfg) => {
        if (cfg.cdn_domain) {
          configStore.getState().setCdnConfig({
            cdnDomain: cfg.cdn_domain,
            // 老服务端省略该字段——false 保持旧行为。
            cdnSigned: cfg.cdn_signed === true,
          });
        }
        configStore.getState().setAuthConfig({
          allowSignup: cfg.allow_signup,
          googleClientId: cfg.google_client_id,
          // 老服务端省略该字段——当作「允许创建」（托管云默认值），而不是
          // 阻塞 UI。
          workspaceCreationDisabled: cfg.workspace_creation_disabled === true,
          // 托管云与老服务端缺失/false → 隐藏该区块。
          vcsIntegrationAvailable: cfg.vcs_integration_available === true,
        });
        configStore.getState().setDaemonConfig({
          daemonServerUrl: cfg.daemon_server_url,
          daemonAppUrl: cfg.daemon_app_url,
        });
        configStore.getState().setFeatureFlags(cfg.feature_flags);
        configStore.getState().setServerVersion(cfg.server_version);
        configStore.getState().setEnvironmentName(cfg.environment_name);
        // 早于 worktree 保存闸门的服务端都缺这个字段——那正是客户端绝不能
        // 提供该模式的时刻（#7113）。
        configStore
          .getState()
          .setLocalWorktreeSupported(cfg.local_worktree_supported === true);
        // 老 agent 处理器会接受这个新增字段却静默丢弃，所以除非服务端显式
        // 声明持久化契约，否则写入保持禁用。
        configStore
          .getState()
          .setAgentConversationStartersSupported(
            cfg.agent_conversation_starters_supported === true,
          );
        configStore
          .getState()
          .setIssueCreatePropertiesSupported(
            cfg.issue_create_properties_supported === true,
          );
        if (cfg.posthog_key) {
          initAnalytics({
            key: cfg.posthog_key,
            host: cfg.posthog_host || "",
            appVersion: identity?.version,
            environment: cfg.analytics_environment,
          });
        }
        configLoadedRef.current = true;
        return true;
      })
      .catch(() => false)
      .finally(() => {
        configRequestRef.current = null;
      });
    configRequestRef.current = request;
    return request;
  }, [identity?.version]);

  useEffect(() => {
    // 最先盖 attribution 戳——注册事件（服务端侧）会读这个 cookie，所以它
    // 必须在用户点提交之前就位。
    captureSignupSource(environmentId);

    // 配置对启动是可选的，但离线启动一旦恢复联网仍须自愈。重试与 auth 相同、
    // 以 30 秒为上限的限速，并在 `online` 事件时加速。
    let cancelled = false;
    let inFlight = false;
    let retryAfterFlight = false;
    let retryIndex = 0;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;

    const attempt = async () => {
      if (cancelled || configLoadedRef.current) return;
      if (retryTimer !== undefined) {
        clearTimeout(retryTimer);
        retryTimer = undefined;
      }
      if (inFlight) {
        retryAfterFlight = true;
        return;
      }

      inFlight = true;
      const loaded = await loadConfig();
      inFlight = false;
      if (cancelled) return;
      if (loaded) {
        window.removeEventListener("online", retryNow);
        return;
      }
      if (retryAfterFlight) {
        retryAfterFlight = false;
        void attempt();
        return;
      }

      const delay = RECOVERY_RETRY_DELAYS_MS[retryIndex];
      retryIndex = Math.min(
        retryIndex + 1,
        RECOVERY_RETRY_DELAYS_MS.length - 1,
      );
      retryTimer = setTimeout(() => {
        retryTimer = undefined;
        void attempt();
      }, delay);
    };

    const retryNow = () => {
      if (cancelled || configLoadedRef.current) return;
      retryIndex = 0;
      if (retryTimer !== undefined) {
        clearTimeout(retryTimer);
        retryTimer = undefined;
      }
      if (inFlight) {
        retryAfterFlight = true;
        return;
      }
      void attempt();
    };

    retryConfigRef.current = retryNow;
    window.addEventListener("online", retryNow);
    void attempt();

    return () => {
      cancelled = true;
      retryConfigRef.current = () => {};
      window.removeEventListener("online", retryNow);
      if (retryTimer !== undefined) clearTimeout(retryTimer);
    };
  }, [loadConfig, environmentId]);

  useEffect(() => {
    const api = getApi();
    let cancelled = false;
    let settled = false;
    let inFlight = false;
    let retryAfterFlight = false;
    let retryIndex = 0;
    let loggedTransientFailure = false;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;

    const onAuthSuccess = (user: User) => {
      onLogin?.();
      useAuthStore.setState({
        user,
        isLoading: false,
        status: "authenticated",
      });
      identifyAnalytics(user.id, { email: user.email, name: user.name });
      if (authRecoveryPendingRef.current) {
        authRecoveryPendingRef.current = false;
        // 网络未就绪的启动可能让 auth 与 config 请求同时失败；auth 成功是
        // 加速 config 恢复的强信号。
        retryConfigRef.current();
      }
    };

    const onAuthFailure = () => {
      onLogout?.();
      resetAnalytics();
      useAuthStore.setState({
        user: null,
        isLoading: false,
        status: "unauthenticated",
      });
    };

    const rejectSession = () => {
      settled = true;
      window.removeEventListener("online", retryNow);
      if (retryTimer !== undefined) clearTimeout(retryTimer);
      if (!cookieAuth) {
        setCurrentWorkspace(null, null);
      }
      onAuthFailure();
    };

    const scheduleRetry = (attempt: () => Promise<void>) => {
      if (cancelled || settled) return;
      const delay = RECOVERY_RETRY_DELAYS_MS[retryIndex];
      retryIndex = Math.min(
        retryIndex + 1,
        RECOVERY_RETRY_DELAYS_MS.length - 1,
      );
      retryTimer = setTimeout(() => {
        retryTimer = undefined;
        void attempt();
      }, delay);
    };

    const warmWorkspaces = () => {
      void qc.fetchQuery(workspaceListOptions()).catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 401) {
          rejectSession();
          return;
        }
        // 工作区消费方直接观察这条查询。React Query 独立于身份认证负责重试与
        // 断线重连。
        logger.error("workspace bootstrap failed", err);
      });
    };

    const attempt = async (): Promise<void> => {
      if (cancelled || settled || inFlight) return;
      if (retryTimer !== undefined) {
        clearTimeout(retryTimer);
        retryTimer = undefined;
      }
      inFlight = true;

      try {
        const user = await api.getMe();
        if (cancelled) return;

        // 身份校验与工作区加载在两个平台上都是独立关注点。立即发布校验过的
        // 用户；路由与外壳消费方拥有共享的工作区查询与它的恢复。
        settled = true;
        window.removeEventListener("online", retryNow);
        onAuthSuccess(user);
        warmWorkspaces();
      } catch (err) {
        if (cancelled) return;
        // /api/me 在「看似有效的会话引用了已不存在的用户」（例如本地数据库
        // 重建之后）时返回 404。这类会话与 401 一样无法靠重试恢复，所以发布
        // 「已登出」状态，而不是打开一个无穷恢复循环。
        if (
          err instanceof ApiError &&
          (err.status === 401 || err.status === 404)
        ) {
          rejectSession();
          return;
        }

        if (!loggedTransientFailure) {
          logger.error("auth init temporarily unavailable", err);
          loggedTransientFailure = true;
        }
        authRecoveryPendingRef.current = true;
        useAuthStore.setState({
          user: null,
          isLoading: true,
          status: "recovering",
        });
        scheduleRetry(attempt);
      } finally {
        inFlight = false;
        if (retryAfterFlight && !cancelled && !settled) {
          retryAfterFlight = false;
          void attempt();
        }
      }
    };

    const retryNow = () => {
      if (cancelled || settled) return;
      retryIndex = 0;
      if (retryTimer !== undefined) {
        clearTimeout(retryTimer);
        retryTimer = undefined;
      }
      if (inFlight) {
        retryAfterFlight = true;
        return;
      }
      void attempt();
    };

    if (!cookieAuth) {
      const token = storage.getItem("multica_token");
      if (!token) {
        settled = true;
        onLogout?.();
        useAuthStore.setState({
          user: null,
          isLoading: false,
          status: "unauthenticated",
        });
      } else {
        api.setToken(token);
        window.addEventListener("online", retryNow);
        void attempt();
      }
    } else {
      window.addEventListener("online", retryNow);
      void attempt();
    }

    return () => {
      cancelled = true;
      window.removeEventListener("online", retryNow);
      if (retryTimer !== undefined) clearTimeout(retryTimer);
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryGeneration]);

  return <>{children}</>;
}
