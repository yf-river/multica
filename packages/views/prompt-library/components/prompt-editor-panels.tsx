import { useMemo, type Dispatch, type SetStateAction } from "react";
import { Loader2, Play } from "lucide-react";
import type { Agent, PromptLibraryItem, PromptLibraryTrial, PromptLibraryVersion } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n/use-t";
import { Field } from "./form-field";
import { allPromptTrialVariablesFilled, summarizePromptTrialVariables } from "./prompt-trial-model";

export function PromptVersionHistory({
  selected,
  versions,
  activeVersionId,
  onSelectVersion,
  loading,
}: {
  selected: PromptLibraryItem | null;
  versions: PromptLibraryVersion[];
  activeVersionId: string | null;
  onSelectVersion: (versionId: string) => void;
  loading: boolean;
}) {
  const { t } = useT("prompt-library");

  if (!selected) {
    return (
      <section className="rounded-md border border-dashed bg-muted/10 px-3 py-3 text-sm text-muted-foreground">
        {t(($) => $.version_history.unsaved)}
      </section>
    );
  }

  return (
    <section className="rounded-md border bg-muted/10 p-3" data-testid="prompt-version-history">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">{t(($) => $.version_history.title)}</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {loading
              ? t(($) => $.version_history.loading)
              : t(($) => $.version_history.summary, { count: versions.length, version: selected.version })}
          </p>
        </div>
      </div>
      {versions.length === 0 ? (
        <div className="mt-3 rounded-md border border-dashed bg-background px-3 py-3 text-sm text-muted-foreground">
          {t(($) => $.version_history.empty)}
        </div>
      ) : (
        <div className="mt-3 grid gap-2">
          {versions.slice(0, 4).map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => onSelectVersion(item.id)}
              className={`grid gap-1 rounded-md border bg-background px-3 py-2 text-left text-xs transition-colors hover:bg-muted/60 md:grid-cols-[minmax(0,1fr)_auto] ${
                activeVersionId === item.id ? "border-primary" : ""
              }`}
            >
              <div className="min-w-0">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">
                    {t(($) => $.version_history.version, { version: item.version })}
                  </span>
                  {item.source_candidate_id && (
                    <span className="text-muted-foreground">
                      {t(($) => $.version_history.candidate, { id: item.source_candidate_id })}
                    </span>
                  )}
                </div>
                {item.change_note && <div className="mt-1 truncate text-foreground">{item.change_note}</div>}
                <div className="mt-1 truncate text-muted-foreground">{item.content}</div>
              </div>
              <div className="text-muted-foreground md:text-right">
                {item.created_at || t(($) => $.shared.unknown_time)}
              </div>
            </button>
          ))}
        </div>
      )}
    </section>
  );
}

export function PromptTrialPanel({
  selected,
  activeVersion,
  agents,
  agentsLoading,
  selectedAgentId,
  onSelectedAgentIdChange,
  variableNames,
  variables,
  onVariablesChange,
  trials,
  trialsLoading,
  running,
  onRun,
}: {
  selected: PromptLibraryItem | null;
  activeVersion: PromptLibraryVersion | null;
  agents: Agent[];
  agentsLoading: boolean;
  selectedAgentId: string;
  onSelectedAgentIdChange: (agentId: string) => void;
  variableNames: string[];
  variables: Record<string, string>;
  onVariablesChange: Dispatch<SetStateAction<Record<string, string>>>;
  trials: PromptLibraryTrial[];
  trialsLoading: boolean;
  running: boolean;
  onRun: () => void;
}) {
  const { t } = useT("prompt-library");
  const agentNameById = useMemo(() => new Map(agents.map((agent) => [agent.id, agent.name])), [agents]);
  const canRun = Boolean(selected && activeVersion && selectedAgentId && allPromptTrialVariablesFilled(variableNames, variables));

  return (
    <section className="rounded-md border bg-muted/10 p-3">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">{t(($) => $.trial.title)}</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {activeVersion
              ? t(($) => $.trial.saved_version, { version: activeVersion.version })
              : t(($) => $.trial.save_first)}
          </p>
        </div>
        <Button size="sm" onClick={onRun} disabled={!canRun || running}>
          {running ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
          {t(($) => $.trial.run)}
        </Button>
      </div>

      <div className="mt-3 grid gap-3 md:grid-cols-[240px_minmax(0,1fr)]">
        <Field label={t(($) => $.trial.agent)}>
          <select
            value={selectedAgentId}
            onChange={(event) => onSelectedAgentIdChange(event.target.value)}
            disabled={!selected || agentsLoading || agents.length === 0}
            className="h-9 w-full rounded-md border bg-background px-2 text-sm"
          >
            {agents.length === 0 ? (
              <option value="">{agentsLoading ? t(($) => $.trial.agents_loading) : t(($) => $.trial.no_agents)}</option>
            ) : (
              agents.map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {agent.name}
                </option>
              ))
            )}
          </select>
        </Field>
        <div className="grid gap-3">
          <div className="text-xs font-medium text-muted-foreground">{t(($) => $.trial.variables)}</div>
          {variableNames.length > 0 ? (
            <div className="grid gap-3 md:grid-cols-2">
              {variableNames.map((name) => (
                <Field key={name} label={t(($) => $.trial.variable, { name })}>
                  <Input
                    value={variables[name] ?? ""}
                    onChange={(event) => {
                      const value = event.target.value;
                      onVariablesChange((current) => ({
                        ...current,
                        [name]: value,
                      }));
                    }}
                    disabled={!selected}
                  />
                </Field>
              ))}
            </div>
          ) : (
            <div className="rounded-md border border-dashed bg-background px-3 py-3 text-sm text-muted-foreground">
              {t(($) => $.trial.no_variables)}
            </div>
          )}
        </div>
      </div>

      <div className="mt-4 border-t pt-3">
        <div className="mb-2 flex items-center justify-between">
          <h4 className="text-xs font-medium text-muted-foreground">{t(($) => $.trial.recent)}</h4>
          {trialsLoading && <Loader2 className="size-3.5 animate-spin text-muted-foreground" />}
        </div>
        {trials.length === 0 ? (
          <div className="rounded-md border border-dashed bg-background px-3 py-3 text-sm text-muted-foreground">
            {t(($) => $.trial.empty)}
          </div>
        ) : (
          <div className="grid gap-2">
            {trials.slice(0, 5).map((trial) => {
              const variableSummary = summarizePromptTrialVariables(trial.variables);
              return (
                <div key={trial.id} className="grid gap-1 rounded-md border bg-background px-3 py-2 text-xs">
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <Badge variant="outline">{trial.status || "queued"}</Badge>
                    <span className="truncate text-foreground">
                      {agentNameById.get(trial.agent_id) ?? t(($) => $.trial.unknown_agent)}
                    </span>
                    <span className="text-muted-foreground">{trial.created_at || t(($) => $.shared.unknown_time)}</span>
                  </div>
                  <div className="truncate text-muted-foreground">
                    {t(($) => $.trial.variables_summary, {
                      value: variableSummary ?? t(($) => $.trial.no_variables_short),
                    })}
                  </div>
                  {trial.output_preview && (
                    <div className="truncate text-muted-foreground">
                      {t(($) => $.trial.output, { value: trial.output_preview })}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </section>
  );
}
