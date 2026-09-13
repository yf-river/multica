// ============================================================================
// 自定义 runtime profile 的「目录」数据构建与表单纯逻辑
//
// 角色定位：自定义 runtime profile 管理对话框（runtime-profiles-dialog）背后
// 的非 UI 部分。回答两类问题：① 目录如何组织——把「内置协议族」（只读参考行）
// 与「用户自定义 profile」（可编辑资产）拼成一个分两组的目录结构，并给出内置
// 协议族的权威白名单；② 命令行如何解析与回填——把用户在「启动命令」里输入的
// 一串 shell 风格文本切分成「命令名 + 固定参数」的严格解析器（含引号/转义/
// 危险字符校验，纯函数、可单测），以及反向的把参数列表拼回命令行的格式化器。
//
// 约定：解析器刻意**不**做 shell 的真实求值——不执行、不展开变量，凡是可能
// 触发 shell 语义的字符（|、>、`、$ 等）一律报错拒绝，因为这里的「命令行」
// 只是最终拼进一条受控命令的固定字符串。
// ============================================================================
import {
  RUNTIME_PROFILE_PROTOCOL_FAMILIES,
  type RuntimeProfile,
  type RuntimeProtocolFamily,
} from "@multica/core/types";

// 目录里的一行：内置协议族以只读参考行的身份出现，自定义 profile 则是用户
// 的可编辑资产。kind 字段让调用方无需检查 profile 是否存在就能分派渲染。
export type RuntimeCatalogEntry =
  | {
      kind: "builtin";
      // 稳定的行 id——对内置行来说协议族本身就充当键。
      id: string;
      protocolFamily: RuntimeProtocolFamily;
    }
  | {
      kind: "custom";
      id: string;
      protocolFamily: RuntimeProtocolFamily;
      profile: RuntimeProfile;
    };

// 目录分两组的容器：customs 是用户资产（主管理区），builtins 是协议族参考行。
export interface RuntimeCatalogSections {
  customs: RuntimeCatalogEntry[];
  builtins: RuntimeCatalogEntry[];
}

// 把内置协议族的白名单重新导出成带类型的数组，让所有调用方（协议族选择器、
// 目录构建器）共享同一个真源。
export const PROTOCOL_FAMILIES: readonly RuntimeProtocolFamily[] =
  RUNTIME_PROFILE_PROTOCOL_FAMILIES;

// 把用户自有的自定义 profile 与内置协议族分开构建目录。对话框把 customs 作为
// 主要管理面渲染，把 builtins 渲染成折叠的参考区。customs 的排序规则：
// 启用中的排在前面，其次按最近更新时间倒序，最后按展示名（不区分大小写）。
export function buildRuntimeCatalog(
  profiles: RuntimeProfile[],
): RuntimeCatalogSections {
  const builtins: RuntimeCatalogEntry[] = PROTOCOL_FAMILIES.map((family) => ({
    kind: "builtin" as const,
    id: `builtin:${family}`,
    protocolFamily: family,
  }));

  const customs: RuntimeCatalogEntry[] = [...profiles]
    .sort((a, b) => {
      if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
      const aTime = Date.parse(a.updated_at) || 0;
      const bTime = Date.parse(b.updated_at) || 0;
      if (aTime !== bTime) return bTime - aTime;
      return a.display_name.localeCompare(b.display_name, undefined, {
        sensitivity: "base",
      });
    })
    .map((profile) => ({
      kind: "custom" as const,
      id: profile.id,
      protocolFamily: profile.protocol_family,
      profile,
    }));

  return { customs, builtins };
}

// 新建/编辑表单收集的原始值（与后端 RuntimeProfile 字段一一对应）。
export interface ProfileFormValues {
  displayName: string;
  commandLine: string;
  description: string;
}

// 表单校验结果的错误字段集合（见 validateProfileForm）。
export type ProfileFormErrorField = "displayName" | "commandLine";

// 命令行解析失败的成因枚举：
//  - empty          输入为空
//  - unclosed_quote 引号没闭合
//  - trailing_escape 反斜杠落在字符串末尾、后面没有可转义的字符
//  - shell_syntax   出现管道/重定向等 shell 控制字符
//  - shell_expansion 出现 $ / 反引号这类变量展开/命令替换触发符
export type CommandLineParseError =
  | "empty"
  | "unclosed_quote"
  | "trailing_escape"
  | "shell_syntax"
  | "shell_expansion";

// 解析结果的可辨识联合：ok 时给出「命令名 + 固定参数」；失败时给出成因。
export type ParsedCommandLine =
  | { ok: true; commandName: string; fixedArgs: string[] }
  | { ok: false; error: CommandLineParseError };

// 处于引号/空白之外的 shell 控制字符——看到它们说明用户塞了会改变语义的
// 东西，直接拒绝而不是猜测意图。
const SHELL_CONTROL_CHARS = new Set(["|", ">", "<", ";", "&"]);

// 把用户输入的启动命令行切成「命令名 + 固定参数」。逐字符扫描的分词器：
// 支持单/双引号分组与反斜杠转义，但**不执行任何 shell 语义**——`|` `;` `&`
// 等控制字符、`$`/反引号等展开符一律报错。若行里有合法的反斜杠转义（如
// 空格），转义后的字符已并入词条，格式化器会再把它引起来还原语义。
export function parseCommandLine(input: string): ParsedCommandLine {
  const line = input.trim();
  if (!line) return { ok: false, error: "empty" };

  const tokens: string[] = [];
  let token = "";
  let quote: "'" | '"' | null = null;
  let tokenStarted = false;

  for (let i = 0; i < line.length; i += 1) {
    const ch = line[i] ?? "";
    const next = line[i + 1] ?? "";

    if (quote == null && /\s/.test(ch)) {
      if (tokenStarted) {
        tokens.push(token);
        token = "";
        tokenStarted = false;
      }
      continue;
    }

    if (quote == null) {
      if (ch === "`" || ch === "$") {
        return {
          ok: false,
          error: ch === "$" ? "shell_expansion" : "shell_syntax",
        };
      }
      if (SHELL_CONTROL_CHARS.has(ch)) {
        return { ok: false, error: "shell_syntax" };
      }
      if (ch === "\\" && next) {
        token += next;
        tokenStarted = true;
        i += 1;
        continue;
      }
      if (ch === "\\") {
        return { ok: false, error: "trailing_escape" };
      }
      if (ch === "'" || ch === '"') {
        quote = ch;
        tokenStarted = true;
        continue;
      }
      token += ch;
      tokenStarted = true;
      continue;
    }

    if (ch === quote) {
      quote = null;
      tokenStarted = true;
      continue;
    }
    if (quote === '"' && ch === "\\" && next) {
      token += next;
      tokenStarted = true;
      i += 1;
      continue;
    }
    if (quote === '"' && ch === "\\") {
      return { ok: false, error: "trailing_escape" };
    }
    if (quote !== "'" && (ch === "`" || ch === "$")) {
      return {
        ok: false,
        error: ch === "$" ? "shell_expansion" : "shell_syntax",
      };
    }
    token += ch;
    tokenStarted = true;
  }

  if (quote != null) return { ok: false, error: "unclosed_quote" };
  if (tokenStarted) tokens.push(token);
  if (tokens.length === 0 || !tokens[0]?.trim()) {
    return { ok: false, error: "empty" };
  }

  return { ok: true, commandName: tokens[0], fixedArgs: tokens.slice(1) };
}

// formatCommandLine 是 parseCommandLine 的逆操作：把「命令名 + 参数」重新拼回
// 一行可供展示/编辑的字符串（parse 时丢失的空白细节在此恢复为规范化形式）。
export function formatCommandLine(commandName: string, fixedArgs: string[]): string {
  return [commandName, ...fixedArgs].filter(Boolean).map(quoteArg).join(" ");
}

// quoteArg：单个参数 → 带引号的字符串。空串给一对空引号；只有含空白、引号
// 或 shell 特殊字符时才用双引号包起来并对引号/反斜杠/$/反引号做转义——完全
// 干净的词则原样返回，避免给用户展示不必要的引号。
function quoteArg(arg: string): string {
  if (arg === "") return '""';
  if (!/[\s"'\\|<>;&`$]/.test(arg)) return arg;
  return `"${arg.replace(/(["\\$`])/g, "\\$1")}"`;
}

// 新建/编辑表单的纯同步校验：返回非法字段的集合（空数组 = 通过）。只有
// 展示名和命令行是必填项；描述与固定参数都是可选的。
export function validateProfileForm(
  values: ProfileFormValues,
): ProfileFormErrorField[] {
  const errors: ProfileFormErrorField[] = [];
  if (!values.displayName.trim()) errors.push("displayName");
  if (!values.commandLine.trim()) errors.push("commandLine");
  return errors;
}

// 该目录条目是否应按内置（只读）行处理。
export function isBuiltinEntry(entry: RuntimeCatalogEntry): boolean {
  return entry.kind === "builtin";
}
