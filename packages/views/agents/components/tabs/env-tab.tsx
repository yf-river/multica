"use client";

// 本文件是 Agent 的环境变量（env）编辑标签页。MUL-2600 之后 agent 资源不再携带
// custom_env 值，只有专用的 `/env` 端点能读，且该端点每次调用都写审计日志——
// 因此本组件绝不自动拉取：显示“配置了几个键”的计数，用户点“查看并编辑”才显值。
// 支持逐行编辑与“整段粘贴/整段编辑”两种模式（共用同一个解析器）。

import { useCallback, useEffect, useState } from "react";
import type { ClipboardEvent } from "react";
import {
  Eye,
  EyeOff,
  List,
  Loader2,
  Lock,
  Plus,
  Save,
  Trash2,
  WrapText,
} from "lucide-react";
import { api } from "@multica/core/api";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { toast } from "sonner";
import { useT } from "../../../i18n";
import type { EnvParseError } from "./env-file";
import {
  formatEnvFile,
  isEnvFilePaste,
  parseEnvFile,
  parseEnvFileResult,
} from "./env-file";

// env 值在用户点“查看并编辑”之前绝不会到达本组件——MUL-2600 后 agent 资源
// feed 已完全不携带 custom_env。此前我们只展示 `agent.custom_env_key_count`
// 的键数，它安全，因为不是值本身。

let nextEnvId = 0;

/** 单条 env 的本地形态：id 稳定供 key 使用，visible 控制值明文/掩码。 */
interface EnvEntry {
  id: number;
  key: string;
  value: string;
  visible: boolean;
}

/** 把后端环境变量映射为本地条目；空 map 给一行空模板。 */
function envMapToEntries(env: Record<string, string>): EnvEntry[] {
  const entries = Object.entries(env).map(([key, value]) => ({
    id: nextEnvId++,
    key,
    value,
    visible: false,
  }));

  return entries.length > 0
    ? entries
    : [{ id: nextEnvId++, key: "", value: "", visible: true }];
}

// 批量模式与粘贴路径走同一个解析器，两个入口永远不可能对“一行是什么意思”
// 产生分歧。
function entriesToBulkText(entries: EnvEntry[]) {
  return formatEnvFile(
    entries
      .filter((entry) => entry.key.trim() !== "")
      .map((entry) => ({ key: entry.key.trim(), value: entry.value })),
  );
}

/** 把条目列表折回普通 map（去掉空 key）。 */
function entriesToEnvMap(entries: EnvEntry[]): Record<string, string> {
  const map = Object.create(null) as Record<string, string>;
  for (const entry of entries) {
    const key = entry.key.trim();
    if (key) {
      map[key] = entry.value;
    }
  }
  return map;
}

/**
 * 环境变量编辑标签页。
 * @param agent 当前 Agent（只读 custom_env_key_count，不读值）。
 * @param onDirtyChange 本地是否有未保存内容的通知。
 * @param onSaved 保存成功后的通知——父页面拥有整个页面要读的 `Agent` 对象
 *  （name、has_custom_env 等），需要它来刷新缓存。可空，简化无失效逻辑的调用点。
 */
export function EnvTab({
  agent,
  onDirtyChange,
  onSaved,
}: {
  agent: Agent;
  onDirtyChange?: (dirty: boolean) => void;
  // Notifier so the parent page can refresh its agent cache after a
  // successful PUT — the parent owns the `Agent` object the rest of
  // the page reads (name, has_custom_env, etc.). Optional so call
  // sites without invalidation logic stay simple.
  onSaved?: () => void;
}) {
  const { t } = useT("agents");

  // revealed === null 表示“还没拉过”；revealed === [] 是拉取成功后合法的空 map。
  // 这里刻意不从 `agent` 预填——agent 资源形态已不带值，只有专用的 `/env` 端点带，
  // 而该端点每次调用都会写审计行，所以绝不在挂载时隐式拉取。
  const [revealed, setRevealed] = useState<EnvEntry[] | null>(null);
  const [originalMap, setOriginalMap] = useState<Record<string, string>>({});
  const [revealing, setRevealing] = useState(false);
  const [saving, setSaving] = useState(false);

  // 批量模式是架在同一份 `revealed` 状态之上的第二个编辑器，而不是独立暂存区：
  // 每个能解析的键击都直接写透，因此脏检测与保存两种模式行为一致。解析不了的键击
  // 让 `revealed` 保持在上一个合法值，同时置 `bulkError` 并禁用保存——用户永远
  // 不可能把屏幕上已经看不见的过期状态保存出去。
  const [bulkEditing, setBulkEditing] = useState(false);
  const [bulkText, setBulkText] = useState("");
  const [bulkError, setBulkError] = useState<EnvParseError | null>(null);

  const keyCount = agent.custom_env_key_count ?? 0;

  const currentEnvMap = revealed ? entriesToEnvMap(revealed) : originalMap;
  const dirty =
    revealed !== null &&
    JSON.stringify(currentEnvMap) !== JSON.stringify(originalMap);

  // 解析不了的批量文本进不了 `revealed`，所以单看 `dirty` 会误报“页面干净”——
  // 而文本域里明明还留着用户的工作；父层的丢弃拦截也会放走一个悄悄丢数据的切页。
  const hasUnsavedWork = dirty || (bulkEditing && bulkError !== null);

  useEffect(() => {
    onDirtyChange?.(hasUnsavedWork);
  }, [hasUnsavedWork, onDirtyChange]);

  const handleReveal = useCallback(async () => {
    setRevealing(true);
    try {
      const resp = await api.getAgentEnv(agent.id);
      const env = resp.custom_env ?? {};
      setOriginalMap(env);
      setRevealed(envMapToEntries(env));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.tab_body.env.reveal_failed_toast),
      );
    } finally {
      setRevealing(false);
    }
  }, [agent.id, t]);

  const addEnvEntry = () => {
    setRevealed((prev) => [
      ...(prev ?? []),
      { id: nextEnvId++, key: "", value: "", visible: true },
    ]);
  };

  const removeEnvEntry = (index: number) => {
    setRevealed((prev) => {
      const entries = (prev ?? []).filter((_, i) => i !== index);
      return entries.length > 0
        ? entries
        : [{ id: nextEnvId++, key: "", value: "", visible: true }];
    });
  };

  const updateEnvEntry = (
    index: number,
    field: "key" | "value",
    val: string,
  ) => {
    setRevealed((prev) =>
      (prev ?? []).map((entry, i) =>
        i === index ? { ...entry, [field]: val } : entry,
      ),
    );
  };

  const describeParseError = (error: EnvParseError) =>
    error.kind === "duplicate"
      ? t(($) => $.tab_body.env.parse_error_duplicate, {
          line: error.line,
          key: error.key,
        })
      : t(($) => $.tab_body.env.parse_error_malformed, { line: error.line });

  const handleEnvPaste = (
    index: number,
    event: ClipboardEvent<HTMLInputElement>,
  ) => {
    const text = event.clipboardData.getData("text/plain");
    const assignments = parseEnvFile(text);
    if (!assignments) {
      if (isEnvFilePaste(text)) {
        event.preventDefault();
        const result = parseEnvFileResult(text);
        toast.error(
          result.ok
            ? t(($) => $.tab_body.env.paste_invalid_toast)
            : describeParseError(result.error),
        );
      }
      return;
    }

    event.preventDefault();
    setRevealed((prev) => {
      const currentEntries = prev ?? [];
      const currentEntry = currentEntries[index];
      const pastedEntries = assignments.map(({ key, value }, pasteIndex) => ({
        id: pasteIndex === 0 && currentEntry ? currentEntry.id : nextEnvId++,
        key,
        value,
        visible: false,
      }));

      return [
        ...currentEntries.slice(0, index),
        ...pastedEntries,
        ...currentEntries.slice(index + 1),
      ];
    });
  };

  const enterBulkEditing = () => {
    const formatted = entriesToBulkText(revealed ?? []);
    if (!formatted.ok) {
      // 宁可拒绝也不回吐一份“读不回去”的文本——否则用户可能去改另一行，
      // 而这一行被静默截断。
      toast.error(
        formatted.reason === "duplicate"
          ? t(($) => $.tab_body.env.duplicate_keys_toast)
          : t(($) => $.tab_body.env.bulk_unsupported_toast, {
              key: formatted.key,
            }),
      );
      return;
    }

    setBulkText(formatted.text);
    setBulkError(null);
    setBulkEditing(true);
  };

  const leaveBulkEditing = () => {
    setBulkEditing(false);
    setBulkError(null);
  };

  const handleBulkTextChange = (text: string) => {
    setBulkText(text);

    const result = parseEnvFileResult(text);
    if (!result.ok) {
      setBulkError(result.error);
      return;
    }

    setBulkError(null);
    setRevealed(
      result.assignments.length > 0
        ? result.assignments.map(({ key, value }) => ({
            id: nextEnvId++,
            key,
            value,
            visible: false,
          }))
        : [{ id: nextEnvId++, key: "", value: "", visible: true }],
    );
  };

  const toggleEnvVisibility = (index: number) => {
    setRevealed((prev) =>
      (prev ?? []).map((entry, i) =>
        i === index ? { ...entry, visible: !entry.visible } : entry,
      ),
    );
  };

  const handleSave = async () => {
    if (revealed === null) return;
    const keys = revealed.filter((e) => e.key.trim()).map((e) => e.key.trim());
    const uniqueKeys = new Set(keys);
    if (uniqueKeys.size < keys.length) {
      toast.error(t(($) => $.tab_body.env.duplicate_keys_toast));
      return;
    }

    setSaving(true);
    try {
      const resp = await api.updateAgentEnv(agent.id, {
        custom_env: currentEnvMap,
      });
      const env = resp.custom_env ?? {};
      const savedEntries = envMapToEntries(env);
      setOriginalMap(env);
      setRevealed(savedEntries);
      // 让文本域与服务端真正接受的保持一致，而不是每次保存都把用户踢出批量模式。
      // 服务端回吐的内容若批量文本表达不了，就退回行编辑——数据已保存，
      // 唯一会错的动作是展示一份有损的文本。
      if (bulkEditing) {
        const formatted = entriesToBulkText(savedEntries);
        if (formatted.ok) {
          setBulkText(formatted.text);
        } else {
          leaveBulkEditing();
        }
      }
      toast.success(t(($) => $.tab_body.env.saved_toast));
      onSaved?.();
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.tab_body.env.save_failed_toast),
      );
    } finally {
      setSaving(false);
    }
  };

  // 未揭示态：显示键数 + “查看并编辑”按钮。绝不挂载即拉取——用户只是在标签间
  // 来回切换不该产生一条审计日志；揭示必须是用户主动行为。
  if (revealed === null) {
    return (
      <div className="space-y-4">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-1">
            <p className="flex items-center gap-2 text-body font-medium">
              <Lock className="h-3.5 w-3.5 text-muted-foreground" />
              {keyCount > 0
                ? t(($) => $.tab_body.env.not_revealed_title, {
                    count: keyCount,
                  })
                : t(($) => $.tab_body.env.not_revealed_empty)}
            </p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.env.not_revealed_hint)}
            </p>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={revealing}
            onClick={handleReveal}
            className="shrink-0"
          >
            {revealing ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Eye className="h-3.5 w-3.5" />
            )}
            {revealing
              ? t(($) => $.tab_body.env.revealing)
              : t(($) => $.tab_body.env.reveal_action)}
          </Button>
        </div>
      </div>
    );
  }

  // Editable state — only entered after a successful reveal.
  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div className="space-y-1 text-caption text-muted-foreground">
          <p>
            {t(($) => $.tab_body.env.intro_prefix)}
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-micro">
              {"ANTHROPIC_API_KEY"}
            </code>
            {t(($) => $.tab_body.env.intro_separator)}
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-micro">
              {"ANTHROPIC_BASE_URL"}
            </code>
            {t(($) => $.tab_body.env.intro_suffix)}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={bulkEditing ? leaveBulkEditing : enterBulkEditing}
            disabled={bulkEditing && bulkError !== null}
          >
            {bulkEditing ? (
              <List className="h-3 w-3" />
            ) : (
              <WrapText className="h-3 w-3" />
            )}
            {bulkEditing
              ? t(($) => $.tab_body.env.row_edit_action)
              : t(($) => $.tab_body.env.bulk_edit_action)}
          </Button>
          {!bulkEditing && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={addEnvEntry}
            >
              <Plus className="h-3 w-3" />
              {t(($) => $.tab_body.common.add)}
            </Button>
          )}
        </div>
      </div>

      {bulkEditing ? (
        <div className="space-y-2">
          <Textarea
            value={bulkText}
            onChange={(e) => handleBulkTextChange(e.target.value)}
            placeholder={t(($) => $.tab_body.env.bulk_placeholder)}
            aria-label={t(($) => $.tab_body.env.bulk_edit_action)}
            aria-invalid={bulkError !== null}
            spellCheck={false}
            className="min-h-48 font-mono text-caption"
          />
          {bulkError ? (
            <p className="text-caption text-destructive">
              {describeParseError(bulkError)}
            </p>
          ) : (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.env.bulk_plaintext_notice)}
            </p>
          )}
        </div>
      ) : revealed.length > 0 ? (
        <div className="space-y-2">
          {revealed.map((entry, index) => (
            <div key={entry.id} className="flex items-center gap-2">
              <Input
                value={entry.key}
                onChange={(e) => updateEnvEntry(index, "key", e.target.value)}
                onPaste={(event) => handleEnvPaste(index, event)}
                placeholder={t(($) => $.tab_body.env.key_placeholder)}
                className="w-[40%] font-mono text-caption"
              />
              <div className="relative flex-1">
                <Input
                  type={entry.visible ? "text" : "password"}
                  value={entry.value}
                  onChange={(e) =>
                    updateEnvEntry(index, "value", e.target.value)
                  }
                  // 值输入框刻意不拦截粘贴：值是任意文本，把 `foo=bar` 粘进值框
                  // 它就是“值”，不是赋值。要粘贴整份文件请用批量编辑。
                  placeholder={t(($) => $.tab_body.env.value_placeholder)}
                  className="pr-8 font-mono text-caption"
                />
                <button
                  type="button"
                  onClick={() => toggleEnvVisibility(index)}
                  className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                  aria-label={
                    entry.visible
                      ? t(($) => $.tab_body.env.hide_value_aria)
                      : t(($) => $.tab_body.env.show_value_aria)
                  }
                >
                  {entry.visible ? (
                    <EyeOff className="h-3.5 w-3.5" />
                  ) : (
                    <Eye className="h-3.5 w-3.5" />
                  )}
                </button>
              </div>
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => removeEnvEntry(index)}
                className="text-muted-foreground hover:text-destructive"
                aria-label={t(($) => $.tab_body.env.remove_aria)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))}
        </div>
      ) : (
        <p className="text-caption italic text-muted-foreground">
          {t(($) => $.tab_body.env.empty_editable)}
        </p>
      )}

      <div className="flex items-center justify-end gap-3">
        {hasUnsavedWork && (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.common.unsaved_changes)}
          </span>
        )}
        <Button
          onClick={handleSave}
          disabled={!dirty || saving || bulkError !== null}
          size="sm"
        >
          {saving ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Save className="h-3.5 w-3.5" />
          )}
          {t(($) => $.tab_body.common.save)}
        </Button>
      </div>
    </div>
  );
}
