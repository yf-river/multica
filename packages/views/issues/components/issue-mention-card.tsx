"use client";

// 本文件：IssueMentionCard —— 可导航的问题提及卡片（编辑器/聊天里的 @问题）。
// 把 IssueChip 包进指向问题详情的 AppLink；hover 打开 IssueHoverCard 展示
// chip 装不下的详情。AppLink 拥有点击语义（普通点击就地导航、修饰键/中键开新
// 页），刻意不做任何按界面或偏好的覆盖。当所指即"当前正在查看的问题"时，
// chip 文字换成"当前问题 · <编号>"以示区别。

import { AppLink } from "../../navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { IssueChip } from "./issue-chip";
import { IssueHoverCard } from "./issue-hover-card";
import { useT } from "../../i18n";
import { useCurrentIssueRenderContext } from "../current-issue-render-context";

interface IssueMentionCardProps {
  issueId: string;
  /** store 里没有该问题时（例如"MUL-7"）的回退文本。 */
  fallbackLabel?: string;
}

function CurrentIssueChipContent({ identifier }: { identifier: string }) {
  const { t } = useT("issues");

  return (
    <span className="font-medium text-muted-foreground shrink-0">
      <span>{t(($) => $.detail.current_issue)}</span>{" "}
      <span className="text-muted-foreground">·</span>{" "}
      <span translate="no">{identifier}</span>
    </span>
  );
}

/**
 * 可导航的问题提及 chip——把 IssueChip 包进指向问题详情的 AppLink。
 * 悬停/光标暗示直接叠在 chip 上，让"看的到的目标"与"点得中的目标"一致。
 *
 * AppLink 拥有点击语义：普通点击就地导航，修饰键/中键开新标签页。
 * 刻意不做任何按界面或按偏好的覆盖。
 *
 * hover 时打开 IssueHoverCard，展示 chip 没有空间放的详情。不传 `delay`，
 * 因此按 Base UI 的默认停留时长触发。chip 兜底用的同一个 `fallbackLabel`
 * 在详情拉取失败时用来给卡片命名。
 */
export function IssueMentionCard({ issueId, fallbackLabel }: IssueMentionCardProps) {
  const p = useWorkspacePaths();
  const currentIssue = useCurrentIssueRenderContext();
  const currentIdentifier =
    currentIssue && issueId === currentIssue.id
      ? currentIssue.identifier
      : null;
  return (
    <IssueHoverCard issueId={issueId} fallbackLabel={fallbackLabel}>
      <AppLink
        href={p.issueDetail(issueId)}
        className="issue-mention align-middle"
      >
        <IssueChip
          issueId={issueId}
          fallbackLabel={fallbackLabel}
          className="cursor-pointer hover:bg-accent transition-colors"
        >
          {currentIdentifier ? (
            <CurrentIssueChipContent identifier={currentIdentifier} />
          ) : undefined}
        </IssueChip>
      </AppLink>
    </IssueHoverCard>
  );
}
