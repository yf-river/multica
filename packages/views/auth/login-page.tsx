"use client";

/**
 * auth 模块：登录页。
 * 支持账号/密码登录，以及 CLI 授权回调流程（验证 localhost 回调地址后，
 * 把带 token 的跳转交回 CLI）。登录成功后预置工作区列表到 React Query 缓存，
 * 再由 onSuccess 决定跳去哪。
 */

import { useState, useEffect, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
  CardFooter,
} from "@multica/ui/components/ui/card";
import { EnvironmentBadge } from "../common/environment-badge";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import { useAuthStore } from "@multica/core/auth";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { api, ApiError } from "@multica/core/api";
import type { User } from "@multica/core/types";
import { useT } from "../i18n";

// ---------------------------------------------------------------------------
// 类型
// ---------------------------------------------------------------------------

/** CLI 授权回调的配置：已校验的回调地址 + 要原样带回 CLI 的 state。 */
interface CliCallbackConfig {
  /** 已校验的 localhost 回调 URL */
  url: string;
  /** 不透明的 state，回传给 CLI */
  state: string;
}

/** 登录页组件的属性。 */
interface LoginPageProps {
  /** 渲染在标题上方的 Logo 元素。 */
  logo?: ReactNode;
  /** 登录成功后调用。触发前工作区列表已种入 React Query，调用方可据此算出
   *  目标 URL。 */
  onSuccess: () => void;
  /** CLI 授权回调配置；传了就走 CLI 授权流程。 */
  cliCallback?: CliCallbackConfig;
  /** 拿到 token 后调用（例如用于种 cookie）。 */
  onTokenObtained?: () => void;
  /** 渲染在登录卡片底部的插槽。 */
  extra?: ReactNode;
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

/** 带着 token/state 跳转到 CLI 回调地址（自动处理 ? 或 & 分隔）。 */
export function redirectToCliCallback(url: string, token: string, state: string) {
  const separator = url.includes("?") ? "&" : "?";
  window.location.href = `${url}${separator}token=${encodeURIComponent(token)}&state=${encodeURIComponent(state)}`;
}

/**
 * 校验 CLI 回调 URL 是否指向一个安全的、走 HTTP 的主机。
 * 允许 localhost 与私网/局域网 IP（RFC 1918），以支持本地虚拟机的自托管部署，
 * 同时拒绝任意公共主机。
 */
export function validateCliCallback(cliCallback: string): boolean {
  try {
    const cbUrl = new URL(cliCallback);
    if (cbUrl.protocol !== "http:") return false;
    const h = cbUrl.hostname;
    if (h === "localhost" || h === "127.0.0.1") return true;
    // 放行 RFC 1918 私网 IP：10.x.x.x、172.16-31.x.x、192.168.x.x
    if (/^10\./.test(h)) return true;
    if (/^172\.(1[6-9]|2\d|3[01])\./.test(h)) return true;
    if (/^192\.168\./.test(h)) return true;
    return false;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// 组件
// ---------------------------------------------------------------------------

/** 登录页：普通账号密码登录，或 CLI 授权确认。 */
export function LoginPage({
  logo,
  onSuccess,
  cliCallback,
  onTokenObtained,
  extra,
}: LoginPageProps) {
  const { t } = useT("auth");
  const qc = useQueryClient();
  const [step, setStep] = useState<"login" | "cli_confirm">("login");
  const [account, setAccount] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [existingUser, setExistingUser] = useState<User | null>(null);

  // 存在 CLI 回调时，先探测是否已有登录会话。
  // Web 认证只有一个真相源：HttpOnly 会话 cookie。
  useEffect(() => {
    if (!cliCallback) return;

    // 探测浏览器 cookie 前先清掉内存中的 bearer，保证 CLI 授权永远不会
    // 继承另一个账号的 token。
    api.setToken(null);

    api
      .getMe()
      .then((user) => {
        setExistingUser(user);
        setStep("cli_confirm");
      })
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.status === 401) return;
        console.warn("[auth] failed to check existing CLI session", err);
      });
  }, [cliCallback]);

  const handleLogin = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!account.trim()) {
      setError(t(($) => $.common.account_required));
      return;
    }
    if (!password) {
      setError(t(($) => $.common.password_required));
      return;
    }
    setLoading(true);
    setError("");
    try {
      if (cliCallback) {
        const { token } = await api.login(account, password);
        onTokenObtained?.();
        redirectToCliCallback(cliCallback.url, token, cliCallback.state);
        return;
      }

      await useAuthStore.getState().login(account, password);
      const wsList = await api.listWorkspaces();
      qc.setQueryData(workspaceKeys.list(), wsList);
      onTokenObtained?.();
      onSuccess();
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : `${t(($) => $.errors.login_failed)} ${t(($) => $.errors.server_unreachable)}`,
      );
      setLoading(false);
    }
  };

  const handleCliAuthorize = async () => {
    if (!cliCallback) return;
    setLoading(true);

    try {
      const { token } = await api.issueCliToken();

      onTokenObtained?.();
      redirectToCliCallback(cliCallback.url, token, cliCallback.state);
    } catch {
      setError(t(($) => $.errors.cli_auth_failed));
      setExistingUser(null);
      setStep("login");
      setLoading(false);
    }
  };

  // -------------------------------------------------------------------------
  // CLI 确认步骤
  // -------------------------------------------------------------------------

  if (step === "cli_confirm" && existingUser) {
    return (
      <div className="flex min-h-svh items-center justify-center">
        <Card className="w-full max-w-sm">
          <CardHeader className="text-center">
          <div className="flex justify-center"><EnvironmentBadge /></div>
            {logo && <div className="mx-auto mb-4">{logo}</div>}
            <CardTitle className="text-display-sm">
              {t(($) => $.cli.title)}
            </CardTitle>
            <CardDescription>
              {t(($) => $.cli.description, { account: existingUser.account })}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <Button
              onClick={handleCliAuthorize}
              disabled={loading}
              className="w-full"
              size="lg"
            >
              {loading
                ? t(($) => $.cli.authorizing)
                : t(($) => $.cli.authorize)}
            </Button>
            <Button
              variant="ghost"
              className="w-full"
              onClick={() => {
                setExistingUser(null);
                setStep("login");
              }}
            >
              {t(($) => $.cli.different_account)}
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="flex min-h-svh items-center justify-center">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <div className="flex justify-center"><EnvironmentBadge /></div>
          {logo && <div className="mx-auto mb-4">{logo}</div>}
          <CardTitle className="text-display-sm">
            {t(($) => $.signin.title)}
          </CardTitle>
          <CardDescription>
            {t(($) => $.signin.description)}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form id="login-form" onSubmit={handleLogin} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="login-account">{t(($) => $.common.account)}</Label>
              <Input
                id="login-account"
                type="text"
                placeholder={t(($) => $.common.account_placeholder)}
                value={account}
                onChange={(e) => setAccount(e.target.value)}
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="login-password">{t(($) => $.common.password)}</Label>
              <Input
                id="login-password"
                type="password"
                placeholder={t(($) => $.common.password_placeholder)}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            {error && (
              <p className="text-body text-destructive">{error}</p>
            )}
          </form>
        </CardContent>
        <CardFooter className="flex flex-col gap-3">
          <Button
            type="submit"
            form="login-form"
            className="w-full"
            size="lg"
            disabled={!account || !password || loading}
          >
            {loading
              ? t(($) => $.signin.signing_in)
              : t(($) => $.signin.continue)}
          </Button>
          {extra && <div className="w-full pt-1 text-center">{extra}</div>}
        </CardFooter>
      </Card>
    </div>
  );
}
