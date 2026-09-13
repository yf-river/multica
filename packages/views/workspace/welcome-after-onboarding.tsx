"use client";

import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { issueKeys } from "@multica/core/issues/queries";
import { useWelcomeStore } from "@multica/core/onboarding";
import { paths, useCurrentWorkspace } from "@multica/core/paths";
import type { CreateIssueRequest, Issue } from "@multica/core/types";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";
import {
  INSTALL_RUNTIME_ISSUE_BODY,
  INSTALL_RUNTIME_ISSUE_TITLE,
} from "../onboarding/templates";

/**
 * workspace 模块：面向「在 onboarding 中明确跳过运行时安装」用户的欢迎弹窗。
 *
 * 只在一次完成 onboarding 时出现：预先创建一个「安装运行时」引导 issue，
 * 再弹完成提示，指引用户去 runtimes 页用真实引导流程补上运行时。
 * 运行时已连接的 onboarding 在进入工作区前就创建了 Mika，永远不会写这个
 * no-runtime 信号。
 */
export function WelcomeAfterOnboarding() {
  const me = useAuthStore((state) => state.user);
  const currentWorkspace = useCurrentWorkspace();
  const signal = useWelcomeStore((state) => state.signal);
  const dismissed = useWelcomeStore((state) => state.dismissed);
  const dismiss = useWelcomeStore((state) => state.dismiss);

  // store 是全局的，而这个组件按工作区作用域。等到匹配的工作区可见后再
  // 去种引导 issue。
  if (
    !me ||
    !signal ||
    dismissed ||
    !currentWorkspace ||
    currentWorkspace.id !== signal.workspaceId
  ) {
    return null;
  }

  return (
    <SkipWelcome
      workspaceId={signal.workspaceId}
      onDismiss={dismiss}
    />
  );
}

/**
 * 模块级去重：避免 React StrictMode 双挂载时对同一个 issue/评论发出两次
 * 相同的创建请求。
 */
const pendingIssueSeed = new Map<string, Promise<Issue>>();

/**
 * 带去重的 issue 预创建：同一 cacheKey 的并发请求共享同一个 Promise。
 */
function seedIssueDeduped(
  cacheKey: string,
  body: CreateIssueRequest,
): Promise<Issue> {
  const existing = pendingIssueSeed.get(cacheKey);
  if (existing) return existing;

  const promise = api.createIssue(body);
  pendingIssueSeed.set(cacheKey, promise);
  promise
    .finally(() => {
      if (pendingIssueSeed.get(cacheKey) === promise) {
        pendingIssueSeed.delete(cacheKey);
      }
    })
    .catch(() => {});
  return promise;
}

interface SkipBundle {
  installIssueId: string;
}

interface SkipWelcomeProps {
  workspaceId: string;
  onDismiss: () => void;
}

/**
 * 在展示完成弹窗之前，先准备好一条聚焦的运行时引导 issue。
 * 一旦出现运行时，Runtimes 页会提供「Start with Mika」并跑与连接型 onboarding
 * 相同的真实引导流程。
 */
function SkipWelcome({ workspaceId, onDismiss }: SkipWelcomeProps) {
  const { t } = useT("onboarding");
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const me = useAuthStore((state) => state.user);

  const [bundle, setBundle] = useState<SkipBundle | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!me || bundle || failed) return;

    let cancelled = false;
    void (async () => {
      try {
        const installRuntime = await seedIssueDeduped(
          `${workspaceId}:install-runtime`,
          {
            title: INSTALL_RUNTIME_ISSUE_TITLE,
            description: INSTALL_RUNTIME_ISSUE_BODY,
            status: "in_progress",
            priority: "high",
            assignee_type: "member",
            assignee_id: me.id,
          },
        );
        void queryClient.invalidateQueries({
          queryKey: issueKeys.all(workspaceId),
        });
        if (!cancelled) {
          setBundle({ installIssueId: installRuntime.id });
        }
      } catch {
        if (!cancelled) setFailed(true);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [bundle, failed, me, queryClient, workspaceId]);

  if (!me) return null;

  // 失败过去是被静默关闭的，没有人能再恢复它：欢迎信号刻意不持久化，
  // onboarding 已标记完成，seedIssueDeduped 的缓存只是一次在途的 Promise——
  // 一次抖动会让成员既没有引导 issue、没有任何提示、也没有回头路。
  // 「重试」会重跑这个 effect（失败标记正是它的闸门），而关闭现在是一个
  // 选择，而不是默认结局。
  if (failed) {
    return (
      <Dialog
        open={true}
        modal={true}
        onOpenChange={(open) => {
          if (!open) onDismiss();
        }}
      >
        <DialogContent className="max-w-md sm:max-w-md">
          <DialogTitle className="text-title font-semibold">
            {t(($) => $.welcome_after_onboarding.skip.error_title)}
          </DialogTitle>
          <DialogDescription className="text-body text-muted-foreground">
            {t(($) => $.welcome_after_onboarding.skip.error_body)}
          </DialogDescription>
          <div className="mt-6 flex justify-end gap-2">
            <Button variant="ghost" onClick={onDismiss}>
              {t(($) => $.welcome_after_onboarding.skip.dismiss)}
            </Button>
            <Button onClick={() => setFailed(false)}>
              {t(($) => $.welcome_after_onboarding.skip.retry)}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    );
  }

  if (!bundle) {
    return (
      <FullScreenLoading
        label={t(($) => $.welcome_after_onboarding.skip.loading)}
      />
    );
  }

  const handleGotIt = async () => {
    onDismiss();
    const slug = await resolveWorkspaceSlug(queryClient, workspaceId);
    navigation.push(paths.workspace(slug).issueDetail(bundle.installIssueId));
  };

  return (
    <Dialog
      open={true}
      modal={true}
      onOpenChange={(open) => {
        if (!open) onDismiss();
      }}
    >
      <DialogContent
        className="max-w-xl sm:max-w-xl"
        aria-describedby="welcome-after-onboarding-skip-subtitle"
      >
        <div className="flex flex-col items-center gap-4 pt-6">
          <div className="text-6xl animate-welcome-emoji-pop" aria-hidden>
            🎉
          </div>
          <DialogTitle className="text-center text-display-sm font-semibold">
            {t(($) => $.welcome_after_onboarding.skip.title)}
          </DialogTitle>
          <DialogDescription
            id="welcome-after-onboarding-skip-subtitle"
            className="text-center text-body text-muted-foreground max-w-md"
          >
            {t(($) => $.welcome_after_onboarding.skip.subtitle)}
          </DialogDescription>
        </div>

        <div className="mt-6 flex flex-col gap-2">
          <SkipPreviewCard
            cardKey="install_runtime"
            statusLabel={t(
              ($) => $.welcome_after_onboarding.skip.status_in_progress,
            )}
          />
        </div>

        <div className="mt-6 flex justify-end">
          <Button size="lg" onClick={handleGotIt}>
            {t(($) => $.welcome_after_onboarding.skip.got_it)}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** 完成弹窗里展示引导卡片（标题 + 状态徽标 + 说明）的预览组件。 */
function SkipPreviewCard({
  cardKey,
  statusLabel,
}: {
  cardKey: "install_runtime";
  statusLabel: string;
}) {
  const { t } = useT("onboarding");

  return (
    <div className="flex items-start gap-3 rounded-lg border bg-background px-3 py-2.5">
      <div className="flex-1 min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-body font-medium leading-tight">
            {t(($) => $.welcome_after_onboarding.skip.cards[cardKey].title)}
          </p>
          <span
            className="rounded-full bg-primary/10 px-2 py-0.5 text-micro font-medium text-primary"
          >
            {statusLabel}
          </span>
        </div>
        <p className="mt-1 text-caption text-muted-foreground leading-snug">
          {t(($) => $.welcome_after_onboarding.skip.cards[cardKey].subtitle)}
        </p>
      </div>
    </div>
  );
}

/** 全屏加载遮罩，用于引导 issue 预创建期间展示。 */
function FullScreenLoading({ label }: { label: string }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-sm">
      <div className="flex flex-col items-center gap-3">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        <p className="text-body text-muted-foreground">{label}</p>
      </div>
    </div>
  );
}

/** 解析工作区 slug：先查缓存里的工作区列表，miss 再回源取一次。 */
async function resolveWorkspaceSlug(
  queryClient: ReturnType<typeof useQueryClient>,
  workspaceId: string,
): Promise<string> {
  const cached = queryClient
    .getQueriesData<{ id: string; slug: string }[] | undefined>({
      queryKey: workspaceKeys.list(),
    })
    .map(([, data]) => data)
    .find(Boolean);
  const hit = cached?.find((workspace) => workspace.id === workspaceId);
  if (hit) return hit.slug;

  const workspace = await api.getWorkspace(workspaceId);
  return workspace.slug;
}
