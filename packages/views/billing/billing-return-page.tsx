"use client";

import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, Loader2, RefreshCw } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { paths } from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";

/**
 * billing 模块：Stripe Checkout 与 Billing Portal 返回的落地页。
 *
 * Stripe 只知道部署时配置的那些 return URL，而一次部署只有一组——所以它们
 * 不能包含工作区 slug。因此 Cloud 追加它已授权的工作区（`workspace_id`）与
 * `result`，本页把它们转成用户真正需要的、基于 slug 的 Settings URL。若把
 * 某一个工作区的 slug 写死在 Stripe 配置里，其它工作区就都回不来了。
 *
 * 本页刻意不做三件事：
 *
 *   - 绝不信任来自查询串的重定向目标。只读 `workspace_id` 与 `result`，目标
 *     一律从 `paths` 重建，因此一个精心构造的返回链接无法变成开放重定向。
 *   - 不自报成功。`result=success` 只表示 Stripe 完成，订阅未必已入账；等待
 *     webhook 的是目标页，本页只是把这个标记转发过去。
 *   - 出错时不会把用户默默丢到别处。付了钱却落在一个莫名其妙的页面，比一句
 *     简短提示更糟，所以下面的失败态会说明发生了什么。
 */

const RESULT_VALUES = new Set(["success", "cancel", "portal"]);
// 与 core 其它地方校验路由 UUID 用同一套形状检查。只有格式正确的 id 才值得
// 带到任何地方——而它可能会进入下面登录绕行时 Google OAuth 的 state。
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Stripe 返回页：把 workspace_id 换成对应 slug 并跳转到计费设置页。 */
export function BillingReturnPage() {
  const { t } = useT("billing");
  const navigation = useNavigation();
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);

  const workspaceIdRaw = navigation.searchParams.get("workspace_id");
  const workspaceIdParam =
    workspaceIdRaw && UUID_RE.test(workspaceIdRaw) ? workspaceIdRaw : null;
  const resultParam = navigation.searchParams.get("result");
  const result = resultParam && RESULT_VALUES.has(resultParam) ? resultParam : null;

  const {
    data: workspaces,
    isPending: isWorkspacesPending,
    isError: isWorkspacesError,
    refetch: refetchWorkspaces,
  } = useQuery({
    ...workspaceListOptions(),
    enabled: !!user,
  });

  const match =
    workspaceIdParam && workspaces
      ? workspaces.find((ws) => ws.id === workspaceIdParam)
      : undefined;
  // 只有列表真的到了之后，「找不到」才有意义。
  const isUnresolvable = !!workspaces && !match;

  useEffect(() => {
    if (isAuthLoading) return;
    // Checkout 可能耗时到会话过期，所以未登录的返回是正常情况而不是错误。
    // 带上足以继续返回现场的信息，这样用户不会落到默认工作区却完全不知道
    // 付款到底成了没有。
    //
    // 从本页真正使用的两个值重建，而不是整串转发：登录页会把 `next` 原样
    // 嵌进 Google OAuth 的 `state`，所以这里剩的任何东西——Stripe 的
    // `session_id`、将来云端的任何参数——都会被没有功能理由地交给第三方。
    // 登录侧 sanitizeNextUrl 也只接受相对路径，因此这不可能变成站外重定向。
    if (!user) {
      const resume = new URLSearchParams();
      if (workspaceIdParam) resume.set("workspace_id", workspaceIdParam);
      if (result) resume.set("result", result);
      const query = resume.toString();
      const returnTo = query
        ? `${navigation.pathname}?${query}`
        : navigation.pathname;
      navigation.replace(`${paths.login()}?next=${encodeURIComponent(returnTo)}`);
      return;
    }
    if (isWorkspacesPending || isWorkspacesError || !workspaces) return;
    if (!match) return;

    const destination = `${paths.workspace(match.slug).settings()}?tab=billing${
      result ? `&result=${result}` : ""
    }`;
    // 用 replace() 而非 push()：Stripe 的中转不该留在历史里，否则一次后退
    // 会重跑这个重定向。
    navigation.replace(destination);
  }, [
    isAuthLoading,
    isWorkspacesError,
    isWorkspacesPending,
    match,
    navigation,
    result,
    user,
    workspaceIdParam,
    workspaces,
  ]);

  // 工作区列表正是把 id 换成 slug 的依据，没有它就没有可送的目的地。
  // 重试好过一个转个没完的 spinner。
  //
  // 下面的文案刻意不声称任何结果。本页无从得知：`cancel` 表示用户中途退出，
  // `portal` 可能是打开了又关闭，连 `success` 也只代表 Stripe 完成——订阅要
  // 等 webhook 落地才算入账。在支付结果页断言「已保存」，是唯一比「去 Billing
  // 看看」更糟的事。
  if (isWorkspacesError) {
    return (
      <ReturnMessage
        title={t(($) => $.return_page.load_failed_title)}
        description={t(($) => $.return_page.load_failed_description)}
        action={
          <Button
            className="h-11"
            variant="outline"
            onClick={() => void refetchWorkspaces()}
          >
            <RefreshCw />
            {t(($) => $.return_page.retry)}
          </Button>
        }
      />
    );
  }

  // 工作区被删、成员身份被撤销、workspace_id 缺失或格式不对、或是手改过的链接。
  if (isUnresolvable) {
    return (
      <ReturnMessage
        title={t(($) => $.return_page.workspace_missing_title)}
        description={t(($) => $.return_page.workspace_missing_description)}
        action={
          <Button className="h-11" onClick={() => navigation.replace(paths.root())}>
            {t(($) => $.return_page.go_to_app)}
          </Button>
        }
      />
    );
  }

  return (
    <div
      className="flex min-h-dvh items-center justify-center bg-background p-6"
      role="status"
      aria-live="polite"
    >
      <div className="flex items-center gap-3 text-body text-muted-foreground">
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />
        <span>{t(($) => $.return_page.redirecting)}</span>
      </div>
    </div>
  );
}

/** 统一的返回消息展示：标题 + 说明 + 操作按钮。 */
function ReturnMessage({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action: React.ReactNode;
}) {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-background p-6">
      <div
        className="flex max-w-[60ch] flex-col items-start gap-3"
        role="alert"
        aria-live="polite"
      >
        <div className="flex items-center gap-2 text-body font-medium">
          <AlertCircle className="size-4 text-destructive" />
          <span>{title}</span>
        </div>
        <p className="text-caption leading-5 text-muted-foreground">
          {description}
        </p>
        {action}
      </div>
    </div>
  );
}
