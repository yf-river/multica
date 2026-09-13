"use client";

/**
 * wecom（企业微信）集成模块：绑定凭据兑换落地页。
 * 企业微信智能机器人「关联你的 Multica 账号」提示指向这里，结构与
 * SlackBindPage 相同。用户落地时可能已登录也可能未登录；兑换前必须登录，
 * 因为兑换者的 Multica 身份取自会话（token 本身从来证明不了是谁在绑定——
 * 见 wecom.BindingTokenService.RedeemAndBind）。
 *
 * token 经 `?token=<raw>` 传入。把它 POST 给 /api/wecom/binding/redeem；后端
 * 返回 410（无效/过期）、409（已绑到别的用户）、403（不是工作区成员）或 200
 * 及已绑定的安装。每种结果经 common.json 里的 wecom_bind 文案映射成不同的
 * 提示。
 */

import { useEffect, useState } from "react";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { AppLink } from "../navigation";
import { useT } from "../i18n";

type RedeemState =
  | { kind: "idle" }
  | { kind: "redeeming" }
  | { kind: "done"; workspaceId: string; installationId: string }
  | { kind: "needs-auth" }
  | { kind: "error"; reason: string };

/** 企业微信绑定页主体：兑换 token 并按结果展示相应状态。 */
export function WecomBindPage({ token }: { token: string | null }) {
  const { t } = useT("common");
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);
  const [state, setState] = useState<RedeemState>({ kind: "idle" });

  useEffect(() => {
    if (!token) {
      setState({ kind: "error", reason: "missing_token" });
      return;
    }
    if (isAuthLoading) return;
    if (!user) {
      setState({ kind: "needs-auth" });
      return;
    }
    if (state.kind !== "idle" && state.kind !== "needs-auth") return;
    setState({ kind: "redeeming" });
    (async () => {
      try {
        const resp = await api.redeemWecomBindingToken(token);
        setState({
          kind: "done",
          workspaceId: resp.workspace_id,
          installationId: resp.installation_id,
        });
      } catch (e) {
        setState({
          kind: "error",
          reason: redemptionFailureReason(e),
        });
      }
    })();
  }, [token, user, isAuthLoading, state.kind]);

  return (
    <div className="mx-auto flex min-h-screen max-w-md flex-col items-center justify-center p-6">
      <Card className="w-full">
        <CardContent className="space-y-4">
          <h1 className="text-title font-semibold">{t(($) => $.wecom_bind.page_title)}</h1>
          {state.kind === "idle" || state.kind === "redeeming" ? (
            <p className="text-body text-muted-foreground">{t(($) => $.wecom_bind.redeeming)}</p>
          ) : state.kind === "needs-auth" ? (
            <>
              <p className="text-body text-muted-foreground">
                {t(($) => $.wecom_bind.needs_auth_description)}
              </p>
              <Button
                size="sm"
                render={
                  <AppLink
                    href={`/login?next=${encodeURIComponent(
                      `/wecom/bind?token=${encodeURIComponent(token ?? "")}`,
                    )}`}
                  />
                }
                nativeButton={false}
              >
                {t(($) => $.wecom_bind.sign_in)}
              </Button>
            </>
          ) : state.kind === "done" ? (
            <>
              <p className="text-body font-medium">{t(($) => $.wecom_bind.done_title)}</p>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.wecom_bind.done_description)}
              </p>
            </>
          ) : (
            <>
              <p className="text-body font-medium">{t(($) => $.wecom_bind.error_title)}</p>
              <p className="text-caption text-muted-foreground">
                {(() => {
                  switch (state.reason) {
                    case "missing_token":
                      return t(($) => $.wecom_bind.error_missing_token);
                    case "expired":
                      return t(($) => $.wecom_bind.error_expired);
                    case "already_bound":
                      return t(($) => $.wecom_bind.error_already_bound);
                    case "not_member":
                      return t(($) => $.wecom_bind.error_not_member);
                    default:
                      return t(($) => $.wecom_bind.error_unknown);
                  }
                })()}
              </p>
              <p className="text-micro text-muted-foreground">
                {t(($) => $.wecom_bind.error_admin_hint)}
              </p>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** 把兑换失败异常归类成一个稳定的原因字符串，供文案分支使用。 */
function redemptionFailureReason(err: unknown): string {
  const msg = err instanceof Error ? err.message : "";
  const lower = msg.toLowerCase();
  if (lower.includes("invalid") || lower.includes("expired") || lower.includes("410")) {
    return "expired";
  }
  if (lower.includes("already bound") || lower.includes("409")) {
    return "already_bound";
  }
  if (lower.includes("workspace member") || lower.includes("403")) {
    return "not_member";
  }
  return "unknown";
}
