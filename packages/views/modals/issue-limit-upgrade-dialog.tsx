// 本文件职责：「issue 数量已达上限」的恢复对话框——居中的全局引导层，手动创建、
// Quick Create、Inbox 等入口共享。展示哪个计费动作完全由云端返回的「可用动作」决定。
"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Gauge, Loader2 } from "lucide-react";
import {
  useCreateWorkspaceSubscriptionPortal,
  workspaceSubscriptionSummaryOptions,
} from "@multica/core/billing";
import { useFeatureEnabled } from "@multica/core/config";
import { BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG } from "@multica/core/feature-flags";
import { useWorkspaceId } from "@multica/core/hooks";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import type { WorkspaceSubscriptionSummary } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";
import { openExternal } from "../platform";

type BillingActions = WorkspaceSubscriptionSummary["availableActions"];
type ModalsT = ReturnType<typeof useT<"modals">>["t"];

/** 生成「创建计费门户会话」的幂等键：前缀 + wsId + 唯一后缀，并截断到 255 字符以内。 */
function createPortalIdempotencyKey(wsId: string): string {
  const suffix =
    globalThis.crypto?.randomUUID?.() ??
    `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `issue-limit-portal-${wsId}-${suffix}`.slice(0, 255);
}

/**
 * issue 上限「恢复」对话框（由手动创建、Quick Create、Inbox 共用）。
 *
 * 该弹窗由 modal store 里的 `issueLimitRecoveryWorkspaceId` 驱动，只在它等于当前
 * 工作区时显示。展示哪个计费动作（去升级 / 打开订阅门户 / 购座 / 联系客服）唯一依据
 * 云端返回的可用动作集合，前端不做任何基于角色、套餐或配额的本机推断。升级成功与否、
 * 后端是否开启计费都会落到不同的文案和按钮组合上。
 */
export function IssueLimitUpgradeDialog() {
  const recoveryWorkspaceId = useModalStore(
    (state) => state.issueLimitRecoveryWorkspaceId,
  );
  const dismiss = useModalStore((state) => state.dismissIssueLimitRecovery);
  const { t } = useT("modals");
  const wsId = useWorkspaceId();
  const visible = recoveryWorkspaceId === wsId;
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const billingEnabled = useFeatureEnabled(
    BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG,
    false,
  );
  const summaryQuery = useQuery({
    ...workspaceSubscriptionSummaryOptions(wsId),
    enabled: visible && billingEnabled,
    staleTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  const createPortal = useCreateWorkspaceSubscriptionPortal(wsId).mutateAsync;
  const portalIntentKeyRef = useRef<string | null>(null);
  const [openingPortal, setOpeningPortal] = useState(false);
  const [portalFailed, setPortalFailed] = useState(false);

  useEffect(() => {
    if (recoveryWorkspaceId !== null && recoveryWorkspaceId !== wsId) {
      dismiss();
    }
  }, [dismiss, recoveryWorkspaceId, wsId]);

  useEffect(() => {
    portalIntentKeyRef.current = null;
    setOpeningPortal(false);
    setPortalFailed(false);
  }, [visible, wsId]);

  const closeForBillingAction = () => {
    dismiss();

    // 恢复入口也可能是从 Inbox 打开的。只有在跟随计费动作时需要顺带关掉底下那个
    // issue 创建弹窗；其它弹窗一律不动。
    const modalStore = useModalStore.getState();
    if (
      modalStore.modal === "create-issue" ||
      modalStore.modal === "quick-create-issue"
    ) {
      modalStore.close();
    }
  };
  const openBilling = () => {
    closeForBillingAction();
    navigation.push(`${paths.settings()}?tab=billing`);
  };
  const openPortal = async () => {
    const key =
      portalIntentKeyRef.current ?? createPortalIdempotencyKey(wsId);
    portalIntentKeyRef.current = key;
    setOpeningPortal(true);
    try {
      const response = await createPortal(key);
      if (!response?.url) {
        setPortalFailed(true);
        return;
      }
      portalIntentKeyRef.current = null;
      closeForBillingAction();
      openExternal(response.url, { webTarget: "same-tab" });
    } catch {
      setPortalFailed(true);
    } finally {
      setOpeningPortal(false);
    }
  };

  const actions = summaryQuery.data?.availableActions;
  const recovery = resolveRecoveryPresentation({
    actions,
    billingEnabled,
    loading: billingEnabled && !actions && summaryQuery.isFetching,
    portalFailed,
    t,
    openBilling,
    openPortal,
  });

  return (
    <Dialog
      open={visible}
      modal
      onOpenChange={(open) => {
        if (!open) dismiss();
      }}
    >
      <DialogContent
        className="gap-0 overflow-hidden p-0 sm:max-w-lg"
        showCloseButton={false}
        onKeyDown={(event) => {
          if (event.key !== "Escape") return;
          event.preventDefault();
          event.stopPropagation();
          dismiss();
        }}
      >
        <div className="px-6 pb-7 pt-8 text-center sm:px-10 sm:pb-8 sm:pt-10">
          <div className="mx-auto flex size-14 items-center justify-center rounded-2xl bg-primary/10 text-primary ring-1 ring-primary/15">
            <Gauge className="size-7" aria-hidden="true" />
          </div>
          <DialogHeader className="mt-5 items-center gap-3 text-center">
            <DialogTitle className="max-w-md text-balance text-display-sm font-semibold leading-tight tracking-tight">
              {t(($) => $.create_issue.issue_limit.title)}
            </DialogTitle>
            <DialogDescription className="max-w-sm text-pretty text-center text-body leading-6">
              {recovery.description}
            </DialogDescription>
          </DialogHeader>
        </div>

        <DialogFooter className="m-0 px-6 py-4 sm:justify-center sm:px-10">
          <Button
            variant={recovery.action ? "outline" : "default"}
            size="lg"
            className="sm:min-w-28"
            onClick={dismiss}
          >
            {t(($) => $.common.close)}
          </Button>
          {recovery.action && (
            <Button
              size="lg"
              className="sm:min-w-40"
              disabled={openingPortal}
              onClick={recovery.action.onClick}
            >
              {openingPortal && <Loader2 className="animate-spin" />}
              {recovery.action.label}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

interface RecoveryPresentation {
  description: string;
  action?: {
    label: string;
    onClick: () => void;
  };
}

/**
 * 根据云端返回的可用动作与本地状态，决定弹窗要展示的「描述文案 + 主按钮」。
 *
 * 判定顺序：计费未开 → 门户打开失败 → 可结账（去升级）→ 可进订阅门户 →
 * 可购座 → 有动作但都不匹配（引导联系）→ 正在查询 → 其余兜底。前几项返回纯文案，
 * 后几项可能附带一个按钮动作。
 */
function resolveRecoveryPresentation({
  actions,
  billingEnabled,
  loading,
  portalFailed,
  t,
  openBilling,
  openPortal,
}: {
  actions: BillingActions | undefined;
  billingEnabled: boolean;
  loading: boolean;
  portalFailed: boolean;
  t: ModalsT;
  openBilling: () => void;
  openPortal: () => Promise<void>;
}): RecoveryPresentation {
  if (!billingEnabled) {
    return {
      description: t(
        ($) => $.create_issue.issue_limit.billing_disabled_description,
      ),
    };
  }

  if (portalFailed) {
    return {
      description: t(
        ($) => $.create_issue.issue_limit.billing_unavailable_description,
      ),
      action: {
        label: t(($) => $.create_issue.issue_limit.billing_action),
        onClick: openBilling,
      },
    };
  }

  if (actions?.checkout) {
    return {
      description: t(($) => $.create_issue.issue_limit.upgrade_description),
      action: {
        label: t(($) => $.create_issue.issue_limit.upgrade_action),
        onClick: openBilling,
      },
    };
  }

  if (actions?.portal) {
    return {
      description: t(($) => $.create_issue.issue_limit.portal_description),
      action: {
        label: t(($) => $.create_issue.issue_limit.portal_action),
        onClick: () => {
          void openPortal();
        },
      },
    };
  }

  if (actions?.purchaseSeats) {
    return {
      description: t(($) => $.create_issue.issue_limit.billing_description),
      action: {
        label: t(($) => $.create_issue.issue_limit.billing_action),
        onClick: openBilling,
      },
    };
  }

  if (actions) {
    return {
      description: t(($) => $.create_issue.issue_limit.contact_description),
    };
  }

  if (loading) {
    return {
      description: t(($) => $.create_issue.issue_limit.checking_description),
    };
  }

  return {
    description: t(
      ($) => $.create_issue.issue_limit.billing_unavailable_description,
    ),
    action: {
      label: t(($) => $.create_issue.issue_limit.billing_action),
      onClick: openBilling,
    },
  };
}
