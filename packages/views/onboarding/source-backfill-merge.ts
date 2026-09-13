// 源回填「合并补丁」：把服务端已存的问卷答案与新填的「来源」字段合成一次
// PATCH 需要发送的完整问卷形状（见 onboarding-flow 的注释）。
import type {
  QuestionnaireAnswers,
  Role,
  UseCase,
} from "@multica/core/onboarding";

/**
 * `PATCH /api/me/onboarding` 会把整个 JSONB 列整体覆盖——并不会做 JSONB 合并
 * （见 `server/internal/handler/onboarding.go` 的 `PatchUserOnboarding`）。
 * 回填流程针对的是已经答过 role / use_case、唯独缺 source 的老账号，这些答案
 * 已存在磁盘上，所以必须先把它们读出来、只叠加来源相关字段，再把完整形状发回去；
 * 否则正是我们要触达的那批用户，会在 Submit / Skip 时把其它答案一并清掉。
 *
 * 兼容说明：与 `OnboardingFlow.mergeQuestionnaire` 以及服务端 `stringOrSlice`
 * 解码器的处理一致——历史单选的 schema 可能把 `source` / `use_case` 存成裸
 * 字符串，这里统一收敛成单元素数组。
 *
 * @param stored 服务端已存的问卷（可能为空）
 * @param sourcePatch 本次要写入的来源字段补丁
 * @returns 合并后的完整问卷（version 固定为 2）
 */
export function mergedQuestionnairePatch(
  stored: Record<string, unknown> | null | undefined,
  sourcePatch: Pick<
    QuestionnaireAnswers,
    "source" | "source_other" | "source_skipped"
  >,
): QuestionnaireAnswers {
  const raw = (stored ?? {}) as Record<string, unknown>;
  return {
    source: sourcePatch.source,
    source_other: sourcePatch.source_other,
    source_skipped: sourcePatch.source_skipped,
    role: coerceRole(raw.role),
    role_other: coerceString(raw.role_other),
    role_skipped: coerceBool(raw.role_skipped),
    use_case: coerceStringArray<UseCase>(raw.use_case),
    use_case_other: coerceString(raw.use_case_other),
    use_case_skipped: coerceBool(raw.use_case_skipped),
    version: 2,
  };
}

function coerceStringArray<T extends string>(value: unknown): T[] {
  if (Array.isArray(value)) {
    return value.filter((v): v is T => typeof v === "string" && v.length > 0);
  }
  if (typeof value === "string" && value.length > 0) {
    return [value as T];
  }
  return [];
}

function coerceString(value: unknown): string | null {
  if (typeof value !== "string") return null;
  return value.length > 0 ? value : null;
}

function coerceBool(value: unknown): boolean {
  return value === true;
}

function coerceRole(value: unknown): Role | null {
  return coerceString(value) as Role | null;
}
