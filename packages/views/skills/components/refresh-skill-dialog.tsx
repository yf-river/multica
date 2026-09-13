// 本文件职责：「从来源重新拉取该 skill」的确认弹窗——展示将覆盖哪些内容并给出警告，
// 确认后调用刷新接口、更新相关缓存。
"use client";

import { useState } from "react";
import { Loader2, RotateCw } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Skill, SkillSummary } from "@multica/core/types";
import { api } from "@multica/core/api";
import {
  skillDetailOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { midTruncate } from "../../common/github-url";
import { useT } from "../../i18n";
import { originSourceUrl, type OriginInfo } from "../lib/origin";

/** 刷新时「要从哪个托管源重新下载」的人类可读名称（GitHub / Skills.sh / ClawHub）。 */
export function useRefreshSourceLabel(origin: OriginInfo | null): string {
  const { t } = useT("skills");
  if (origin?.type === "clawhub") return t(($) => $.detail.refresh.source_clawhub);
  if (origin?.type === "skills_sh") return t(($) => $.detail.refresh.source_skills_sh);
  return t(($) => $.detail.refresh.source_github);
}

/**
 * 对应 `POST /api/skills/:id/refresh` 的「确认并执行」弹窗。服务端会用上游的最新状态
 * 替换 name、description、SKILL.md 与全部文件，同时保留 skill id——因此 agent 的
 * 挂载、标签与创建人都不会丢。本地改动不保留，所以需要先确认。
 */
export function RefreshSkillDialog({
  skill,
  origin,
  wsId,
  open,
  onOpenChange,
  onRefreshed,
}: {
  skill: SkillSummary;
  origin: OriginInfo | null;
  wsId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 缓存更新后触发；详情页用这版数据接管展示。 */
  onRefreshed?: (updated: Skill) => void;
}) {
  const { t } = useT("skills");
  const qc = useQueryClient();
  const [refreshing, setRefreshing] = useState(false);
  const source = useRefreshSourceLabel(origin);
  // 只有「通过校验的 URL」才能变成链接；被注入或与来源类型不符的 source_url 在确认
  // 弹窗里就渲染成空，而不是一个活锚点。
  const sourceUrl = originSourceUrl(origin);

  const handleConfirm = async () => {
    setRefreshing(true);
    try {
      const updated = await api.refreshSkill(skill.id);
      if (updated.id === skill.id) {
        qc.setQueryData(skillDetailOptions(wsId, skill.id).queryKey, updated);
      } else {
        // schema 兜底（畸形响应）：直接丢弃过期缓存，而不是用空占位去种它。
        qc.invalidateQueries({
          queryKey: skillDetailOptions(wsId, skill.id).queryKey,
        });
      }
      qc.invalidateQueries({ queryKey: workspaceKeys.skills(wsId), exact: true });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.success(t(($) => $.detail.refresh.toast_success, { source }));
      onOpenChange(false);
      if (updated.id === skill.id) onRefreshed?.(updated);
    } catch (e) {
      toast.error(
        e instanceof Error && e.message
          ? e.message
          : t(($) => $.detail.refresh.toast_failed),
      );
    } finally {
      setRefreshing(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!refreshing) onOpenChange(v);
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.detail.refresh.dialog.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.detail.refresh.dialog.description, {
              name: skill.name,
              source,
            })}
          </DialogDescription>
        </DialogHeader>
        {sourceUrl && (
          <Tooltip>
            <TooltipTrigger
              render={
                <a
                  href={sourceUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="w-fit max-w-full truncate text-caption text-muted-foreground hover:underline"
                >
                  {midTruncate(sourceUrl, 60)}
                </a>
              }
            />
            <TooltipContent side="top">{sourceUrl}</TooltipContent>
          </Tooltip>
        )}
        <div className="rounded-md bg-warning/10 px-3 py-2 text-caption text-muted-foreground">
          {t(($) => $.detail.refresh.dialog.warning)}
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={refreshing}
          >
            {t(($) => $.detail.refresh.dialog.cancel)}
          </Button>
          <Button type="button" onClick={handleConfirm} disabled={refreshing}>
            {refreshing ? (
              <>
                <Loader2 className="h-3 w-3 animate-spin" />
                {t(($) => $.detail.refresh.dialog.refreshing)}
              </>
            ) : (
              <>
                <RotateCw className="h-3 w-3" />
                {t(($) => $.detail.refresh.dialog.confirm)}
              </>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
