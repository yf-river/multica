"use client";

// 本文件是“给 Agent 挂工作区技能”的添加弹窗：多选 + 明确点“确认”才生效。
// 早期版本是点一行就立即挂上，用户既没法一次勾好几个，也没机会复核就把弹窗关了。

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent, SkillSummary } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  skillListOptions,
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
import { useT } from "../../i18n";
import { SkillPickerList } from "./skill-picker-list";

/**
 * “给 Agent 添加工作区技能”的弹窗。多选 + 明确点确认才提交。
 *
 * 已挂在 Agent 上的技能会被整体从列表里剔除（而不是置灰显示）。当工作区里已没有
 * 可挂的新技能时，空态文案会解释原因，且确认按钮因无从可选而天然处于禁用态。
 */
export function SkillAddDialog({
  agent,
  open,
  onOpenChange,
}: {
  agent: Agent;
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data: workspaceSkills = [], isLoading } = useQuery(skillListOptions(wsId));
  const [saving, setSaving] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());

  const attachedIds = useMemo(
    () => new Set(agent.skills.map((s) => s.id)),
    [agent.skills],
  );
  // 已挂上的技能直接从列表里剔除——这个弹窗只负责“加新的”。
  // 想回看 Agent 已有技能，看上面 SkillsTab 的列表即可。
  const availableSkills = useMemo(
    () => workspaceSkills.filter((s) => !attachedIds.has(s.id)),
    [workspaceSkills, attachedIds],
  );

  const handleOpenChange = (v: boolean) => {
    if (!v) setSelectedIds(new Set());
    onOpenChange(v);
  };

  const handleToggle = (skill: SkillSummary) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(skill.id)) next.delete(skill.id);
      else next.add(skill.id);
      return next;
    });
  };

  const handleConfirm = async () => {
    if (selectedIds.size === 0) return;
    setSaving(true);
    try {
      const newIds = [
        ...agent.skills.map((s) => s.id),
        ...selectedIds,
      ];
      await api.setAgentSkills(agent.id, { skill_ids: newIds });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      handleOpenChange(false);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.tab_body.skills.add_failed_toast));
    } finally {
      setSaving(false);
    }
  };

  const count = selectedIds.size;

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle className="text-body">
            {t(($) => $.tab_body.skills.add_dialog_title)}
          </DialogTitle>
          <DialogDescription className="text-caption">
            {t(($) => $.tab_body.skills.add_dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <SkillPickerList
          skills={availableSkills}
          selectedIds={selectedIds}
          onToggle={handleToggle}
          loading={isLoading}
          emptyMessage={
            workspaceSkills.length === 0
              ? t(($) => $.tab_body.skills.add_dialog_empty)
              : t(($) => $.tab_body.skills.add_dialog_empty_partial)
          }
        />

        <DialogFooter>
          <Button variant="ghost" onClick={() => handleOpenChange(false)}>
            {t(($) => $.tab_body.skills.add_dialog_cancel)}
          </Button>
          <Button
            onClick={handleConfirm}
            disabled={count === 0 || saving}
          >
            {saving
              ? t(($) => $.tab_body.skills.add_dialog_saving)
              : count > 0
                ? t(($) => $.tab_body.skills.add_dialog_confirm, { count })
                : t(($) => $.tab_body.skills.add_dialog_confirm_default)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
