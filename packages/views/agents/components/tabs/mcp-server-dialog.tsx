"use client";

// 本文件是“新增/编辑 MCP 服务”的弹窗：引导式表单只能表达 stdio / http 两种传输，
// 其余一律走 JSON 编辑（原样往返）。名字可锁定（后端不支持原子重命名时），
// 名称格式限定为 [A-Za-z0-9_-]，保存前做去重校验。

import { useEffect, useMemo, useState } from "react";
import { Loader2, Plus, Trash2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../../i18n";
import type { ManagedMcpServer } from "./mcp-config-model";
import { isRecord, mcpTransport } from "./mcp-config-model";

type EditorMode = "form" | "json";
type FormTransport = "stdio" | "http";
/** 键值对（env、headers）的行编辑形态。 */
type KeyValue = { key: string; value: string };

/** 表单态的全部字段；extras 承载表单表达不了的额外配置键。 */
type McpFormState = {
  transport: FormTransport;
  command: string;
  args: string[];
  env: KeyValue[];
  url: string;
  headers: KeyValue[];
  extras: Record<string, unknown>;
};

/** 一张空表单（stdio、命令为空、无附加项）。 */
const emptyForm = (): McpFormState => ({
  transport: "stdio",
  command: "",
  args: [],
  env: [],
  url: "",
  headers: [],
  extras: {},
});

/** 把记录折成键值对行（只保留字符串值；非字符串进 extras 由 JSON 编辑负责）。 */
function pairsFromRecord(value: unknown): KeyValue[] {
  if (!isRecord(value)) return [];
  return Object.entries(value).flatMap(([key, item]) =>
    typeof item === "string" ? [{ key, value: item }] : [],
  );
}

/** 把已存配置灌进表单：识别传输、命令/参数、env、url、headers，其余进 extras。 */
function formFromConfig(config: Record<string, unknown>): McpFormState {
  const extras = { ...config };
  for (const key of [
    "type",
    "command",
    "args",
    "env",
    "environment",
    "url",
    "headers",
  ]) {
    delete extras[key];
  }

  const transport = mcpTransport(config) === "stdio" ? "stdio" : "http";
  let command = "";
  let args: string[] = [];
  if (typeof config.command === "string") command = config.command;
  else if (Array.isArray(config.command)) {
    const tokens = config.command.filter(
      (value): value is string => typeof value === "string",
    );
    command = tokens[0] ?? "";
    args = tokens.slice(1);
  }
  if (Array.isArray(config.args)) {
    args = config.args.filter(
      (value): value is string => typeof value === "string",
    );
  }

  return {
    transport,
    command,
    args,
    env: pairsFromRecord(config.env ?? config.environment),
    url: typeof config.url === "string" ? config.url : "",
    headers: pairsFromRecord(config.headers),
    extras,
  };
}

/** 键值对列表回写为记录（空 key 剔除；无有效条目返回 undefined）。 */
function recordFromPairs(pairs: KeyValue[]): Record<string, string> | undefined {
  const entries = pairs
    .map(({ key, value }) => [key.trim(), value] as const)
    .filter(([key]) => key !== "");
  return entries.length > 0 ? Object.fromEntries(entries) : undefined;
}

/**
 * 引导式表单只能表达两种传输，且从表单保存会 REWRITE 整条（`configFromForm` 对任何
 * 非 stdio 都发 `type: "http"`）。因此任何其它传输的条目——今天是 `sse`，明天是新
 * 后端报的什么——都绝不能走表单编辑，否则一改就悄悄换掉协议、可能直接弄坏服务。
 * 这些条目走 JSON 路径，原样往返用户输入的文本。
 *
 * 这里分类的是“不含密钥的摘要传输类型”，服务端从一个封闭集合（stdio / http /
 * sse / unknown）产生。
 */
function formCanExpressTransport(transport: string): boolean {
  return transport === "stdio" || transport === "http";
}

/** `configFromForm` 写回时不会改变其含义的 `type` 值集合。 */
const FORM_EXPRESSIBLE_TYPES = new Set([
  "local",
  "stdio",
  "remote",
  "http",
  "streamable-http",
]);

/**
 * 表单能否“往返”一条已保存的条目。
 *
 * 刻意不复用 `mcpTransport`：那是有损的“展示用”分类器——凡带 `url` 的条目一律
 * 报 “http”，不管其 `type` 是什么——所以 `{"type":"websocket","url":"wss://…"}`
 * 会被误判为可表单编辑，保存时被改写成 `type: "http"`。安全决策需要显式的
 * `type` 值，而不是展示桶。
 */
function formCanExpressConfig(config: Record<string, unknown>): boolean {
  const type =
    typeof config.type === "string" ? config.type.trim().toLowerCase() : "";
  if (type !== "") return FORM_EXPRESSIBLE_TYPES.has(type);
  // 无显式 type：表单从 `command` 推断 stdio、从 `url` 推断 http，并按同形写回。
  // 两者皆无时表单无从表达，交给 JSON 编辑器。
  return config.command !== undefined || config.url !== undefined;
}

/** 把表单态折回配置记录（stdio 写 command/args/env，http 写 type/url/headers，外加 extras）。 */
function configFromForm(form: McpFormState): Record<string, unknown> {
  const config = { ...form.extras };
  if (form.transport === "stdio") {
    config.command = form.command.trim();
    if (form.args.length > 0) config.args = form.args;
    const env = recordFromPairs(form.env);
    if (env) config.env = env;
  } else {
    config.type = "http";
    config.url = form.url.trim();
    const headers = recordFromPairs(form.headers);
    if (headers) config.headers = headers;
  }
  return config;
}

/** 解析用户粘贴的服务 JSON：必须为对象且带 command 或 url（保证可执行目标）。 */
function parseServerJson(text: string):
  | { ok: true; value: Record<string, unknown> }
  | { ok: false; error: string } {
  try {
    const value = JSON.parse(text);
    if (!isRecord(value)) return { ok: false, error: "not_object" };
    if (!value.command && !value.url) return { ok: false, error: "missing_target" };
    return { ok: true, value };
  } catch (error) {
    return {
      ok: false,
      error: error instanceof Error ? error.message : "invalid JSON",
    };
  }
}

/**
 * 引导式表单能否“不改动地”编辑这条。旧版 `mcp` 容器是 provider 原生的、必须逐字
 * 往返；表单表达不了的传输同理。调用方读不回已存配置时，安全摘要里的传输类型是
 * 唯一信号。
 */
function formSupportsServer(server: ManagedMcpServer): boolean {
  if (server.container === "mcp") return false;
  // 拿得到已存条目就判条目本身；摘要传输只是给读不回条目的调用方兜底。
  return Object.keys(server.config).length > 0
    ? formCanExpressConfig(server.config)
    : formCanExpressTransport(server.transport);
}

/**
 * 新增/编辑 MCP 服务的弹窗。新增时用表单；编辑既有条目时，若表单无法无损表达
 * 该条目（如 sse 传输、`mcp` 容器），自动落在 JSON 页签并锁定该页签。
 * @param server 正在编辑的条目；null = 新增。
 * @param existingNames 已存在的名称集合，用于保存前的重名校验。
 * @param lockName 编辑时锁定名称输入。后端不支持原子重命名时必须置 true——
 *   放开名字框会把“保存”变成“再建一台服务”，还把原来的悄悄留在原地。
 */
export function McpServerDialog({
  open,
  server,
  existingNames,
  lockName = false,
  onOpenChange,
  onSave,
  description,
  onDescriptionChange,
}: {
  open: boolean;
  server: ManagedMcpServer | null;
  existingNames: Set<string>;
  /**
   * Pins the name while editing. Callers whose backend has no atomic rename
   * must set this: letting the field change would turn "save" into "create a
   * second server" and silently leave the original behind.
   */
  lockName?: boolean;
  onOpenChange: (open: boolean) => void;
  description?: string;
  onDescriptionChange?: (value: string) => void;
  onSave: (name: string, config: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const [name, setName] = useState("");
  const [mode, setMode] = useState<EditorMode>("form");
  const [form, setForm] = useState<McpFormState>(emptyForm);
  const [jsonText, setJsonText] = useState("{}");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    const config = server?.config ?? {};
    setName(server?.name ?? "");
    // 读不回已存条目的调用方（工作区层是“只写”的）会传空 config + 安全摘要的
    // 传输类型。此时若用 `formFromConfig({})` 播种，会把一台已知的 stdio 服务
    // 开成一个 http 表单——所以传输类型要取自摘要。
    setForm(
      !server
        ? emptyForm()
        : Object.keys(config).length > 0
          ? formFromConfig(config)
          : { ...emptyForm(), transport: server.transport === "stdio" ? "stdio" : "http" },
    );
    setJsonText(JSON.stringify(config, null, 2));
    setMode(server && !formSupportsServer(server) ? "json" : "form");
  }, [open, server]);

  // 对表单表达不了的条目，表单是“不可用”（而非仅仅未选中），所以切过去也改不了它。
  const formAvailable = !server || formSupportsServer(server);

  const jsonResult = useMemo(() => parseServerJson(jsonText), [jsonText]);
  const trimmedName = name.trim();
  const nameError =
    trimmedName === ""
      ? "required"
      : !/^[A-Za-z0-9_-]+$/.test(trimmedName)
        ? "format"
        : existingNames.has(trimmedName) && trimmedName !== server?.name
          ? "duplicate"
          : null;
  const formError =
    form.transport === "stdio"
      ? form.command.trim() === ""
        ? "command"
        : null
      : form.url.trim() === ""
        ? "url"
        : null;
  const canSave =
    !saving &&
    nameError === null &&
    (mode === "form" ? formError === null : jsonResult.ok);

  const errorMessage =
    nameError === "required"
      ? t(($) => $.tab_body.mcp_config.dialog_name_required)
      : nameError === "format"
        ? t(($) => $.tab_body.mcp_config.dialog_name_invalid)
        : nameError === "duplicate"
          ? t(($) => $.tab_body.mcp_config.dialog_name_duplicate)
          : mode === "form" && formError === "command"
            ? t(($) => $.tab_body.mcp_config.dialog_command_required)
            : mode === "form" && formError === "url"
              ? t(($) => $.tab_body.mcp_config.dialog_url_required)
              : mode === "json" && !jsonResult.ok && jsonResult.error === "not_object"
                ? t(($) => $.tab_body.mcp_config.dialog_json_object)
                : mode === "json" && !jsonResult.ok && jsonResult.error === "missing_target"
                  ? t(($) => $.tab_body.mcp_config.dialog_json_target)
                  : mode === "json" && !jsonResult.ok
                    ? t(($) => $.tab_body.mcp_config.invalid_json, {
                        error: jsonResult.error,
                      })
                    : "";

  const handleModeChange = (next: string | number | null) => {
    if (next !== "form" && next !== "json") return;
    if (next === "json" && mode === "form") {
      setJsonText(JSON.stringify(configFromForm(form), null, 2));
    } else if (next === "form" && mode === "json" && jsonResult.ok) {
      setForm(formFromConfig(jsonResult.value));
    }
    setMode(next);
  };

  const handleSave = async () => {
    if (!canSave) return;
    let config: Record<string, unknown>;
    if (mode === "form") config = configFromForm(form);
    else if (jsonResult.ok) config = jsonResult.value;
    else return;
    setSaving(true);
    try {
      await onSave(trimmedName, config);
      onOpenChange(false);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !saving && onOpenChange(next)}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {server
              ? t(($) => $.tab_body.mcp_config.dialog_edit_title)
              : t(($) => $.tab_body.mcp_config.dialog_add_title)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.tab_body.mcp_config.dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label htmlFor="mcp-server-name">
            {t(($) => $.tab_body.mcp_config.dialog_name_label)}
          </Label>
          <Input
            id="mcp-server-name"
            name="mcp-server-name"
            autoComplete="off"
            spellCheck={false}
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={t(($) => $.tab_body.mcp_config.dialog_name_placeholder)}
            aria-invalid={nameError !== null || undefined}
            readOnly={lockName}
            aria-readonly={lockName || undefined}
            className={lockName ? "text-muted-foreground" : undefined}
          />
          {lockName ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.mcp_config.dialog_name_locked)}
            </p>
          ) : null}
        </div>

        {onDescriptionChange && <div className="space-y-2"><Label htmlFor="mcp-description">{t(($) => $.tab_body.mcp_config.description_label)}</Label><Input id="mcp-description" value={description ?? ""} onChange={(e) => onDescriptionChange(e.target.value)} /></div>}
        <Tabs value={mode} onValueChange={handleModeChange}>
          <TabsList className="grid w-full grid-cols-2">
            <TabsTrigger value="form" disabled={!formAvailable}>
              {t(($) => $.tab_body.mcp_config.dialog_form_tab)}
            </TabsTrigger>
            <TabsTrigger value="json">
              {t(($) => $.tab_body.mcp_config.dialog_json_tab)}
            </TabsTrigger>
          </TabsList>

          <TabsContent value="form" className="space-y-5 pt-2">
            <fieldset className="space-y-2">
              <legend className="text-body font-medium">
                {t(($) => $.tab_body.mcp_config.dialog_type_label)}
              </legend>
              <div className="grid grid-cols-2 gap-2">
                {(["stdio", "http"] as const).map((transport) => (
                  <Button
                    key={transport}
                    type="button"
                    variant={form.transport === transport ? "secondary" : "outline"}
                    aria-pressed={form.transport === transport}
                    onClick={() => setForm((current) => ({ ...current, transport }))}
                  >
                    {transport === "stdio"
                      ? t(($) => $.tab_body.mcp_config.dialog_type_stdio)
                      : t(($) => $.tab_body.mcp_config.dialog_type_http)}
                  </Button>
                ))}
              </div>
            </fieldset>

            {form.transport === "stdio" ? (
              <>
                <div className="space-y-2">
                  <Label htmlFor="mcp-server-command">
                    {t(($) => $.tab_body.mcp_config.dialog_command_label)}
                  </Label>
                  <Input
                    id="mcp-server-command"
                    name="mcp-server-command"
                    autoComplete="off"
                    spellCheck={false}
                    value={form.command}
                    onChange={(event) =>
                      setForm((current) => ({ ...current, command: event.target.value }))
                    }
                    // An example command the user types, not copy.
                    // eslint-disable-next-line no-restricted-syntax
                    placeholder="npx"
                    aria-invalid={formError === "command" || undefined}
                  />
                </div>
                <StringListEditor
                  label={t(($) => $.tab_body.mcp_config.dialog_args_label)}
                  addLabel={t(($) => $.tab_body.mcp_config.dialog_add_arg)}
                  removeLabel={t(($) => $.tab_body.mcp_config.dialog_remove_arg)}
                  values={form.args}
                  onChange={(args) => setForm((current) => ({ ...current, args }))}
                />
                <KeyValueEditor
                  label={t(($) => $.tab_body.mcp_config.dialog_env_label)}
                  addLabel={t(($) => $.tab_body.mcp_config.dialog_add_env)}
                  removeLabel={t(($) => $.tab_body.mcp_config.dialog_remove_env)}
                  rows={form.env}
                  onChange={(env) => setForm((current) => ({ ...current, env }))}
                />
              </>
            ) : (
              <>
                <div className="space-y-2">
                  <Label htmlFor="mcp-server-url">
                    {t(($) => $.tab_body.mcp_config.dialog_url_label)}
                  </Label>
                  <Input
                    id="mcp-server-url"
                    name="mcp-server-url"
                    type="url"
                    inputMode="url"
                    autoComplete="off"
                    spellCheck={false}
                    value={form.url}
                    onChange={(event) =>
                      setForm((current) => ({ ...current, url: event.target.value }))
                    }
                    placeholder="https://mcp.example.com/mcp"
                    aria-invalid={formError === "url" || undefined}
                  />
                </div>
                <KeyValueEditor
                  label={t(($) => $.tab_body.mcp_config.dialog_headers_label)}
                  addLabel={t(($) => $.tab_body.mcp_config.dialog_add_header)}
                  removeLabel={t(($) => $.tab_body.mcp_config.dialog_remove_header)}
                  rows={form.headers}
                  onChange={(headers) =>
                    setForm((current) => ({ ...current, headers }))
                  }
                />
              </>
            )}
          </TabsContent>

          <TabsContent value="json" className="space-y-2 pt-2">
            {server?.container === "mcp" && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.mcp_config.dialog_native_json_hint)}
              </p>
            )}
            <Label htmlFor="mcp-server-json">
              {t(($) => $.tab_body.mcp_config.dialog_json_label)}
            </Label>
            <Textarea
              id="mcp-server-json"
              name="mcp-server-json"
              autoComplete="off"
              spellCheck={false}
              rows={12}
              className="min-h-64 resize-y font-mono text-caption"
              value={jsonText}
              onChange={(event) => setJsonText(event.target.value)}
              aria-invalid={!jsonResult.ok || undefined}
              aria-label={t(($) => $.tab_body.mcp_config.dialog_json_aria)}
            />
          </TabsContent>
        </Tabs>

        {errorMessage && (
          <p className="text-caption text-destructive" aria-live="polite">
            {errorMessage}
          </p>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={saving}>
            {t(($) => $.tab_body.mcp_config.dialog_cancel)}
          </Button>
          <Button onClick={handleSave} disabled={!canSave}>
            {saving && (
              <Loader2
                className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
            )}
            {server
              ? t(($) => $.tab_body.mcp_config.dialog_update_action)
              : t(($) => $.tab_body.mcp_config.dialog_add_action)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** 字符串列表编辑器（命令参数 args）：增删行、就地编辑。 */
function StringListEditor({
  label,
  addLabel,
  removeLabel,
  values,
  onChange,
}: {
  label: string;
  addLabel: string;
  removeLabel: string;
  values: string[];
  onChange: (values: string[]) => void;
}) {
  return (
    <fieldset className="space-y-2">
      <legend className="text-body font-medium">{label}</legend>
      {values.map((value, index) => (
        <div key={index} className="flex gap-2">
          <Input
            aria-label={`${label} ${index + 1}`}
            name={`mcp-argument-${index}`}
            autoComplete="off"
            spellCheck={false}
            value={value}
            onChange={(event) =>
              onChange(values.map((item, itemIndex) =>
                itemIndex === index ? event.target.value : item,
              ))
            }
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`${removeLabel} ${index + 1}`}
            onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))}
          >
            <Trash2 aria-hidden="true" />
          </Button>
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" onClick={() => onChange([...values, ""])}>
        <Plus aria-hidden="true" />
        {addLabel}
      </Button>
    </fieldset>
  );
}

/** 键值对编辑器（env、headers）：键值两列 + 删除，可增行。 */
function KeyValueEditor({
  label,
  addLabel,
  removeLabel,
  rows,
  onChange,
}: {
  label: string;
  addLabel: string;
  removeLabel: string;
  rows: KeyValue[];
  onChange: (rows: KeyValue[]) => void;
}) {
  // `label` / `addLabel` / `removeLabel` 由调用方翻译好再传，但逐行的文案（含
  // aria-label）是本组件自己的——曾几何时 aria-label 把一个英文词拼到已翻译的
  // 标签后面，读出来半中半英。
  const { t } = useT("agents");
  return (
    <fieldset className="space-y-2">
      <legend className="text-body font-medium">{label}</legend>
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_1fr_auto] gap-2">
          <Input
            aria-label={t(($) => $.tab_body.mcp_config.dialog_pair_key_aria, {
              label,
              index: index + 1,
            })}
            name={`mcp-pair-key-${index}`}
            autoComplete="off"
            spellCheck={false}
            value={row.key}
            onChange={(event) =>
              onChange(rows.map((item, itemIndex) =>
                itemIndex === index ? { ...item, key: event.target.value } : item,
              ))
            }
            placeholder={t(
              ($) => $.tab_body.mcp_config.dialog_pair_key_placeholder,
            )}
          />
          <Input
            aria-label={t(($) => $.tab_body.mcp_config.dialog_pair_value_aria, {
              label,
              index: index + 1,
            })}
            name={`mcp-pair-value-${index}`}
            autoComplete="off"
            spellCheck={false}
            value={row.value}
            onChange={(event) =>
              onChange(rows.map((item, itemIndex) =>
                itemIndex === index ? { ...item, value: event.target.value } : item,
              ))
            }
            placeholder={t(
              ($) => $.tab_body.mcp_config.dialog_pair_value_placeholder,
            )}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`${removeLabel} ${index + 1}`}
            onClick={() => onChange(rows.filter((_, itemIndex) => itemIndex !== index))}
          >
            <Trash2 aria-hidden="true" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange([...rows, { key: "", value: "" }])}
      >
        <Plus aria-hidden="true" />
        {addLabel}
      </Button>
    </fieldset>
  );
}
