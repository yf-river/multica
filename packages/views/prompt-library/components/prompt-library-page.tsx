"use client";

import { useEffect, useMemo, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpenText, Loader2, Plus, Save, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { renderPromptTemplate } from "@multica/core/prompt-library";
import type {
  CreatePromptLibraryItemRequest,
  PromptLibraryItem,
  PromptLibraryStatus,
  PromptLibraryVariable,
  UpdatePromptLibraryItemRequest,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Badge } from "@multica/ui/components/ui/badge";
import { PageHeader } from "../../layout/page-header";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";

const promptLibraryKeys = {
  list: (workspaceId: string) => ["prompt-library", workspaceId, "list"] as const,
};

const PROMPT_TYPES = ["全部", "需求澄清", "系统提示词", "评测提示词", "小队 SOP", "通用"];

const USER_CENTER_TEMPLATE: CreatePromptLibraryItemRequest = {
  name: "user-center 需求澄清提示词",
  description: "user-center 小队队长使用",
  prompt_type: "需求澄清",
  content: "请先澄清目标、边界、验收条件、风险、影响范围和可观测指标。输出必须使用中文，并列出需要团队确认的问题。",
  variables: [
    { name: "issue_title", label: "issue 标题", required: true },
    { name: "project_context", label: "项目背景" },
  ],
  tags: ["user-center", "小队", "需求澄清"],
  status: "启用",
};

type PromptDraft = {
  name: string;
  description: string;
  prompt_type: string;
  content: string;
  variablesText: string;
  tagsText: string;
  status: PromptLibraryStatus;
};

const emptyDraft = (): PromptDraft => ({
  name: "",
  description: "",
  prompt_type: "通用",
  content: "",
  variablesText: "",
  tagsText: "",
  status: "启用",
});

export function PromptLibraryPage() {
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("全部");
  const [statusFilter, setStatusFilter] = useState<"全部" | PromptLibraryStatus>("全部");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [draft, setDraft] = useState<PromptDraft>(emptyDraft);
  const [debugValuesText, setDebugValuesText] = useState("");

  const listQuery = useQuery({
    queryKey: promptLibraryKeys.list(workspaceId ?? ""),
    queryFn: () => api.listPromptLibraryItems(),
    enabled: !!workspaceId,
  });

  const items = listQuery.data?.items ?? [];
  const selected = selectedId ? items.find((item) => item.id === selectedId) ?? null : null;

  useEffect(() => {
    if (!selected && selectedId && items.length > 0) {
      setSelectedId(items[0]?.id ?? null);
    }
  }, [items, selected, selectedId]);

  useEffect(() => {
    if (!selected) return;
    setDraft(itemToDraft(selected));
    setDebugValuesText(valuesToDebugText(selected.variables));
  }, [selected]);

  const filteredItems = useMemo(() => {
    const q = query.trim().toLowerCase();
    return items.filter((item) => {
      if (typeFilter !== "全部" && item.prompt_type !== typeFilter) return false;
      if (statusFilter !== "全部" && item.status !== statusFilter) return false;
      if (!q) return true;
      const haystack = [item.name, item.description, item.prompt_type, item.content, ...item.tags].join(" ");
      return haystack.toLowerCase().includes(q) || matchesPinyin(haystack, q);
    });
  }, [items, query, statusFilter, typeFilter]);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.list(workspaceId ?? "") });

  const createMut = useMutation({
    mutationFn: (data: CreatePromptLibraryItemRequest) => api.createPromptLibraryItem(data),
    onSuccess: (item) => {
      invalidate();
      setSelectedId(item.id);
      toast.success("提示词已创建");
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptLibraryItemRequest }) => api.updatePromptLibraryItem(id, data),
    onSuccess: (item) => {
      invalidate();
      setSelectedId(item.id);
      toast.success("提示词已保存");
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deletePromptLibraryItem(id),
    onSuccess: () => {
      invalidate();
      setSelectedId(null);
      setDraft(emptyDraft());
      toast.success("提示词已删除");
    },
  });

  const saving = createMut.isPending || updateMut.isPending;
  const deleting = deleteMut.isPending;
  const debugResult = useMemo(
    () => renderPromptTemplate({
      content: draft.content,
      variables: parseVariables(draft.variablesText),
      values: parseDebugValues(debugValuesText),
    }),
    [debugValuesText, draft.content, draft.variablesText],
  );

  const startNew = () => {
    setSelectedId(null);
    setDraft(emptyDraft());
    setDebugValuesText("");
  };

  const applyUserCenterTemplate = () => {
    setSelectedId(null);
    setDraft(requestToDraft(USER_CENTER_TEMPLATE));
    setDebugValuesText(valuesToDebugText(USER_CENTER_TEMPLATE.variables ?? []));
  };

  const saveDraft = () => {
    const payload = draftToRequest(draft);
    if (!payload.name.trim()) {
      toast.error("请输入名称");
      return;
    }
    if (!payload.content.trim()) {
      toast.error("请输入提示词内容");
      return;
    }
    if (selected) {
      updateMut.mutate({ id: selected.id, data: payload });
    } else {
      createMut.mutate(payload);
    }
  };

  const toggleArchive = () => {
    if (!selected) return;
    updateMut.mutate({
      id: selected.id,
      data: { status: selected.status === "启用" ? "归档" : "启用" },
    });
  };

  const deleteSelected = () => {
    if (!selected) return;
    if (!window.confirm(`删除提示词「${selected.name}」？`)) return;
    deleteMut.mutate(selected.id);
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <BookOpenText className="size-4 shrink-0 text-muted-foreground" />
          <h1 className="truncate text-sm font-semibold">提示词库</h1>
          <span className="text-xs text-muted-foreground">{items.length}</span>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="secondary" onClick={applyUserCenterTemplate}>
            <BookOpenText className="size-3.5" />
            user-center 模板
          </Button>
          <Button size="sm" onClick={startNew}>
            <Plus className="size-3.5" />
            新建
          </Button>
        </div>
      </PageHeader>

      <div className="flex min-h-0 flex-1 flex-col md:grid md:grid-cols-[360px_minmax(0,1fr)]">
        <aside className="flex min-h-0 flex-col border-b md:border-b-0 md:border-r">
          <div className="space-y-3 border-b p-3">
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="搜索名称、标签、内容"
                className="h-8 pl-8 text-sm"
              />
            </div>
            <div className="flex flex-wrap gap-1.5">
              {PROMPT_TYPES.map((type) => (
                <FilterButton key={type} active={typeFilter === type} onClick={() => setTypeFilter(type)}>
                  {type}
                </FilterButton>
              ))}
            </div>
            <div className="flex flex-wrap gap-1.5">
              {(["全部", "启用", "归档"] as const).map((status) => (
                <FilterButton key={status} active={statusFilter === status} onClick={() => setStatusFilter(status)}>
                  {status}
                </FilterButton>
              ))}
            </div>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto">
            {listQuery.isLoading ? (
              <div className="space-y-2 p-3">
                {Array.from({ length: 5 }).map((_, index) => (
                  <div key={index} className="h-16 rounded-md bg-muted/60" />
                ))}
              </div>
            ) : filteredItems.length === 0 ? (
              <div className="p-6 text-sm text-muted-foreground">暂无提示词</div>
            ) : (
              <div className="divide-y">
                {filteredItems.map((item) => (
                  <button
                    key={item.id}
                    type="button"
                    onClick={() => setSelectedId(item.id)}
                    className={`flex w-full flex-col gap-2 px-3 py-3 text-left transition-colors hover:bg-muted/60 ${
                      selectedId === item.id ? "bg-muted" : ""
                    }`}
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="min-w-0 flex-1 truncate text-sm font-medium">{item.name}</span>
                      <Badge variant={item.status === "启用" ? "secondary" : "outline"} className="shrink-0">
                        {item.status}
                      </Badge>
                    </div>
                    <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                      <span className="shrink-0">{item.prompt_type}</span>
                      <span className="truncate">{item.description || "无描述"}</span>
                    </div>
                    {item.tags.length > 0 && (
                      <div className="flex flex-wrap gap-1">
                        {item.tags.slice(0, 4).map((tag) => (
                          <span key={tag} className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                            {tag}
                          </span>
                        ))}
                      </div>
                    )}
                  </button>
                ))}
              </div>
            )}
          </div>
        </aside>

        <main className="min-h-0 overflow-y-auto p-4 md:p-6">
          <div className="mx-auto flex max-w-5xl flex-col gap-4">
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0">
                <h2 className="truncate text-base font-semibold">{selected ? selected.name : "新建提示词"}</h2>
                <div className="mt-1 text-xs text-muted-foreground">
                  {selected ? `版本 ${selected.version} · ${selected.updated_at}` : "未保存"}
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {selected && (
                  <>
                    <Button size="sm" variant="secondary" onClick={toggleArchive} disabled={saving}>
                      <Archive className="size-3.5" />
                      {selected.status === "启用" ? "归档" : "启用"}
                    </Button>
                    <Button size="sm" variant="destructive" onClick={deleteSelected} disabled={deleting}>
                      <Trash2 className="size-3.5" />
                      删除
                    </Button>
                  </>
                )}
                <Button size="sm" onClick={saveDraft} disabled={saving}>
                  {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                  保存
                </Button>
              </div>
            </div>

            <div className="grid gap-4 md:grid-cols-2">
              <Field label="名称">
                <Input value={draft.name} onChange={(event) => setDraftField(setDraft, "name", event.target.value)} />
              </Field>
              <Field label="类型">
                <Input value={draft.prompt_type} onChange={(event) => setDraftField(setDraft, "prompt_type", event.target.value)} />
              </Field>
              <Field label="描述">
                <Input value={draft.description} onChange={(event) => setDraftField(setDraft, "description", event.target.value)} />
              </Field>
              <Field label="状态">
                <div className="flex h-10 items-center gap-2">
                  {(["启用", "归档"] as const).map((status) => (
                    <FilterButton key={status} active={draft.status === status} onClick={() => setDraftField(setDraft, "status", status)}>
                      {status}
                    </FilterButton>
                  ))}
                </div>
              </Field>
              <Field label="变量">
                <Input
                  value={draft.variablesText}
                  onChange={(event) => setDraftField(setDraft, "variablesText", event.target.value)}
                  placeholder="issue_title=issue 标题, project_context=项目背景"
                />
              </Field>
              <Field label="标签">
                <Input
                  value={draft.tagsText}
                  onChange={(event) => setDraftField(setDraft, "tagsText", event.target.value)}
                  placeholder="user-center, 小队, 需求澄清"
                />
              </Field>
            </div>

            <Field label="提示词内容">
              <Textarea
                value={draft.content}
                onChange={(event) => setDraftField(setDraft, "content", event.target.value)}
                className="min-h-[360px] resize-y font-mono text-sm leading-6"
              />
            </Field>

            <section className="grid gap-4 md:grid-cols-[320px_minmax(0,1fr)]">
              <Field label="调试变量">
                <Textarea
                  value={debugValuesText}
                  onChange={(event) => setDebugValuesText(event.target.value)}
                  className="min-h-[180px] resize-y font-mono text-sm leading-6"
                  placeholder="issue_title=登录失败&#10;project_context=user-center"
                />
              </Field>
              <div className="grid gap-1.5 text-sm">
                <div className="flex min-h-5 items-center gap-2">
                  <span className="text-xs font-medium text-muted-foreground">调试输出</span>
                  {debugResult.missingVariables.length > 0 && (
                    <Badge variant="outline" className="text-[11px]">
                      缺失 {debugResult.missingVariables.join("、")}
                    </Badge>
                  )}
                </div>
                <pre className="min-h-[180px] overflow-auto whitespace-pre-wrap rounded-md border bg-muted/20 p-3 font-mono text-sm leading-6">
                  {debugResult.rendered || "暂无输出"}
                </pre>
              </div>
            </section>
          </div>
        </main>
      </div>
    </div>
  );
}

function FilterButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex h-7 items-center rounded-md border px-2.5 text-xs transition-colors ${
        active ? "border-foreground bg-foreground text-background" : "border-border bg-background text-muted-foreground hover:text-foreground"
      }`}
    >
      {children}
    </button>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1.5 text-sm">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

function setDraftField<K extends keyof PromptDraft>(
  setDraft: Dispatch<SetStateAction<PromptDraft>>,
  key: K,
  value: PromptDraft[K],
) {
  setDraft((current) => ({ ...current, [key]: value }));
}

function itemToDraft(item: PromptLibraryItem): PromptDraft {
  return {
    name: item.name,
    description: item.description,
    prompt_type: item.prompt_type,
    content: item.content,
    variablesText: variablesToText(item.variables),
    tagsText: item.tags.join(", "),
    status: item.status,
  };
}

function requestToDraft(req: CreatePromptLibraryItemRequest): PromptDraft {
  return {
    name: req.name,
    description: req.description ?? "",
    prompt_type: req.prompt_type ?? "通用",
    content: req.content,
    variablesText: variablesToText(req.variables ?? []),
    tagsText: (req.tags ?? []).join(", "),
    status: req.status ?? "启用",
  };
}

function draftToRequest(draft: PromptDraft): CreatePromptLibraryItemRequest {
  return {
    name: draft.name.trim(),
    description: draft.description.trim(),
    prompt_type: draft.prompt_type.trim() || "通用",
    content: draft.content,
    variables: parseVariables(draft.variablesText),
    tags: splitList(draft.tagsText),
    status: draft.status,
  };
}

function variablesToText(variables: PromptLibraryVariable[]): string {
  return variables.map((variable) => `${variable.name}${variable.label ? `=${variable.label}` : ""}`).join(", ");
}

function valuesToDebugText(variables: PromptLibraryVariable[]): string {
  return variables.map((variable) => `${variable.name}=${variable.default_value ?? ""}`).join("\n");
}

function parseVariables(value: string): PromptLibraryVariable[] {
  return splitList(value).map((part) => {
    const [name, ...labelParts] = part.split("=");
    const label = labelParts.join("=").trim();
    const variableName = (name ?? "").trim();
    return {
      name: variableName,
      ...(label ? { label } : {}),
    };
  }).filter((variable) => variable.name.length > 0);
}

function splitList(value: string): string[] {
  return value.split(/[,，]/).map((part) => part.trim()).filter(Boolean);
}

function parseDebugValues(value: string): Record<string, string> {
  const result: Record<string, string> = {};
  for (const line of value.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const [name, ...valueParts] = trimmed.split("=");
    const key = (name ?? "").trim();
    if (!key) continue;
    result[key] = valueParts.join("=").trim();
  }
  return result;
}
