"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId, useWorkspacePaths } from "@multica/core/paths";
import { supervisionKeys } from "@multica/core/supervision/queries";
import type { SupervisionConfig } from "@multica/core/supervision/schema";
import { Button } from "@multica/ui/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import { AppLink } from "../../../navigation";
import { useT } from "../../../i18n";
import { errorText, moment } from "./supervision-shared";

type Props = { open: boolean; close: () => void; config: SupervisionConfig };

/** Settings sheet: agent, scope, cadence, and the read-only investigation switch. */
export function SupervisionSettings({ open, close, config }: Props) {
  const { t } = useT("run-reviews");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const [agentId, setAgentId] = useState<string | null>(null);
  const [interval, setInterval] = useState<number | null>(null);
  const [investigation, setInvestigation] = useState<boolean | null>(null);
  const agents = useQuery({ queryKey: [...supervisionKeys.all(wsId), "agents"], queryFn: () => api.listAgents(), enabled: open });
  const selectedAgent = agents.data?.find((agent) => agent.id === (agentId ?? config.agent_id));
  const instructionsMissing = selectedAgent ? !selectedAgent.instructions.trim() : false;
  const save = useMutation({
    mutationFn: (enabled: boolean) => api.updateSupervision({
      agent_id: agentId ?? config.agent_id,
      enabled,
      interval_seconds: interval ?? config.interval_seconds,
      investigation_enabled: investigation ?? (config.agent_id ? config.investigation_enabled : true),
    }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) });
      close();
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <Sheet open={open} onOpenChange={(value) => { if (!value) close(); }}>
      <SheetContent className="!w-full overflow-y-auto sm:!max-w-lg">
        <SheetHeader>
          <SheetTitle>{t(($) => $.supervision.settings)}</SheetTitle>
          <SheetDescription>{t(($) => $.supervision.settings_description)}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 p-4">
          <div className="rounded-lg border bg-muted/30 p-3 text-body">
            <p className="font-medium">{config.error ? t(($) => $.supervision.interrupted) : config.enabled ? t(($) => $.supervision.active) : t(($) => $.supervision.paused)}</p>
            <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.supervision.last_success)} {moment(config.last_success_at)}</p>
          </div>
          <label className="block space-y-2 text-body" htmlFor="supervision-agent">
            <span>{t(($) => $.supervision.agent)}</span>
            <select
              id="supervision-agent"
              className="h-10 w-full rounded-md border bg-background px-3"
              value={agentId ?? config.agent_id ?? ""}
              onChange={(e) => setAgentId(e.target.value)}
            >
              <option value="">{t(($) => $.supervision.select_agent)}</option>
              {agents.data?.filter((agent) => !agent.archived_at).map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
            </select>
          </label>
          {selectedAgent && (
            <p className={`text-caption ${instructionsMissing ? "text-warning" : "text-muted-foreground"}`}>
              {instructionsMissing ? t(($) => $.supervision.agent_instructions_empty) : t(($) => $.supervision.agent_instructions_filled, { count: selectedAgent.instructions.trim().length })}
              {instructionsMissing && (
                <AppLink href={paths.agentDetail(selectedAgent.id)} className="ml-1 underline underline-offset-4">
                  {t(($) => $.supervision.agent_instructions_link)}
                </AppLink>
              )}
            </p>
          )}
          {agents.isError && <p role="alert">{errorText(agents.error)}</p>}
          <div className="text-body">
            <p className="font-medium">{t(($) => $.supervision.scope)}</p>
            <p className="mt-1 text-muted-foreground">{t(($) => $.supervision.scope_description)}</p>
          </div>
          <label className="block space-y-2 text-body" htmlFor="supervision-interval">
            <span>{t(($) => $.supervision.interval)}</span>
            <select
              id="supervision-interval"
              className="h-10 w-full rounded-md border bg-background px-3"
              value={interval ?? config.interval_seconds}
              onChange={(e) => setInterval(Number(e.target.value))}
            >
              <option value={60}>{t(($) => $.supervision.interval_short)}</option>
              <option value={300}>{t(($) => $.supervision.minute_five)}</option>
              <option value={900}>{t(($) => $.supervision.minute_fifteen)}</option>
            </select>
          </label>
          <label className="flex items-start gap-3 rounded-lg border p-3 text-body">
            <input
              type="checkbox"
              className="mt-1 size-4 shrink-0"
              checked={investigation ?? (config.agent_id ? config.investigation_enabled : true)}
              onChange={(e) => setInvestigation(e.target.checked)}
            />
            <span>
              <span className="block font-medium">{t(($) => $.supervision.investigation_enable)}</span>
              <span className="mt-1 block text-caption text-muted-foreground">{t(($) => $.supervision.investigation_hint)}</span>
            </span>
          </label>
          <p className="text-caption text-muted-foreground">{t(($) => $.supervision.interval_hint)}</p>
          <p className="text-caption text-muted-foreground">{t(($) => $.supervision.feedback_manual)}</p>
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={save.isPending || !(agentId ?? config.agent_id) || instructionsMissing}
              onClick={() => save.mutate(true)}
            >
              {save.isPending ? t(($) => $.supervision.saving) : t(($) => $.supervision.save_enable)}
            </Button>
            {config.enabled && <Button variant="outline" disabled={save.isPending} onClick={() => save.mutate(false)}>{t(($) => $.supervision.pause)}</Button>}
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
