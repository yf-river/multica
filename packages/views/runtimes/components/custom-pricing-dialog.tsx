// ============================================================================
// 自定义定价对话框（CustomPricingDialog）——给「模型名 → 每百万 token 单价」覆写
//
// 角色定位：usage-section 定价栏打开的弹窗，让用户为某个模型手动填写四档单价
// （输入 / 输出 / 缓存读 / 缓存写，单位：美元 / 百万 token），覆盖内置费率表。
// 数据落在 zustand 的 useCustomPricingStore（跨页面持久），KPI 与图表在计算时会
// 优先读这里的覆盖价。
//
// 行为细节：
//  - 列表 = 「当前无法定价的模型」∪「已在 store 里的模型」，前者保证用户能立刻
//    处理自己来此想修的模型，后者保证保存过的覆盖价还能再改 / 删。
//  - 编辑期间价格存成字符串：用户可以把某格暂时清空而不会被提前当成 0 吞掉输入。
//  - 四格全空 = 删除这条覆盖价；点行内垃圾桶可立即删除并移除 override。
// ============================================================================
"use client";

import { useEffect, useState } from "react";
import { Trash2 } from "lucide-react";
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
  useCustomPricingStore,
  type CustomModelPricing,
} from "@multica/core/runtimes/custom-pricing-store";
import { useT } from "../../i18n";

// 一行的四个「每百万 token 单价」草稿。编辑期间以字符串保存——这样用户清空某一格
// 时我们能暂时保留空值，而不是立刻回填 0、把还没输完的数字藏起来。
type DraftRow = {
  input: string;
  output: string;
  cacheRead: string;
  cacheWrite: string;
};

const EMPTY_DRAFT: DraftRow = { input: "", output: "", cacheRead: "", cacheWrite: "" };

// 把 store 里的结构化价格转回编辑用的字符串草稿；没有记录就是全空行。
function toDraft(p: CustomModelPricing | undefined): DraftRow {
  if (!p) return EMPTY_DRAFT;
  return {
    input: String(p.input),
    output: String(p.output),
    cacheRead: String(p.cacheRead),
    cacheWrite: String(p.cacheWrite),
  };
}

// 把字符串草稿解析成数字价格。任一格不是有限数字或为负，整行判无效返回 null，
// 保存时跳过该行（清空整行的语义见 handleSave）。
function parseRow(draft: DraftRow): CustomModelPricing | null {
  const values = [draft.input, draft.output, draft.cacheRead, draft.cacheWrite].map(
    (s) => Number(s.trim()),
  );
  if (values.some((n) => !Number.isFinite(n) || n < 0)) return null;
  const [input, output, cacheRead, cacheWrite] = values as [
    number,
    number,
    number,
    number,
  ];
  return { input, output, cacheRead, cacheWrite };
}

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // 当前被标记为「无法定价」的模型——它们总会出现在表单里，保证用户能立刻给
  // 自己来此想修的模型填价。已经存在于自定义定价 store 里的模型也会展示（用于
  // 继续编辑 / 删除旧条目），哪怕它们现在已经不是「无法定价」的状态。
  unmappedModels: readonly string[];
}

/**
 * 自定义定价对话框：编辑指定模型的每百万 token 覆盖单价。
 *
 * @param open - 是否展示。
 * @param onOpenChange - 开关状态回调。
 * @param unmappedModels - 当前定价表里查不到的模型名；它们 + store 里已有条目
 *   共同组成可编辑的行集合。
 *
 * 保存语义：行内四格全空 = 删除该模型覆盖价；格子有内容但解析失败的行被跳过；
 * 无输入框的行（unmapped 但尚未填价）保持不动。保存后立即关闭弹窗。
 */
export function CustomPricingDialog({ open, onOpenChange, unmappedModels }: Props) {
  const { t } = useT("runtimes");
  const pricings = useCustomPricingStore((s) => s.pricings);
  const setCustomPricing = useCustomPricingStore((s) => s.setCustomPricing);
  const removeCustomPricing = useCustomPricingStore((s) => s.removeCustomPricing);

  // 行集合 = 全部「无法定价」模型 ∪ store 里已有的覆盖条目——这样用户保存后
  // 再次打开对话框，仍能看到 / 修改 / 删除之前的记录，而不是凭空消失。
  const rows = Array.from(
    new Set([...unmappedModels, ...Object.keys(pricings)]),
  ).sort();

  const [drafts, setDrafts] = useState<Record<string, DraftRow>>({});

  // 每次打开（或打开期间可见行集合变化）都重置草稿，避免上一次会话残留的半截
  // 输入混进新一次编辑。
  useEffect(() => {
    if (!open) return;
    const fresh: Record<string, DraftRow> = {};
    for (const key of rows) {
      fresh[key] = toDraft(pricings[key]);
    }
    setDrafts(fresh);
    // 故意不把 `rows`（每次渲染都是新数组）列为依赖——依赖 `open` 加上拼成串的
    // 模型列表已经足够，还避免了父组件每次重渲染都把草稿清掉。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, rows.join("\n")]);

  const updateField = (key: string, field: keyof DraftRow, value: string) => {
    setDrafts((d) => ({
      ...d,
      [key]: { ...(d[key] ?? EMPTY_DRAFT), [field]: value },
    }));
  };

  const handleSave = () => {
    for (const key of rows) {
      const draft = drafts[key] ?? EMPTY_DRAFT;
      const parsed = parseRow(draft);
      const allEmpty =
        draft.input.trim() === "" &&
        draft.output.trim() === "" &&
        draft.cacheRead.trim() === "" &&
        draft.cacheWrite.trim() === "";
      if (allEmpty) {
        // 四格全清空 = 明确表达「不要这条覆盖价」——store 里有就删掉。
        if (pricings[key]) removeCustomPricing(key);
        continue;
      }
      // 有内容的行解析失败（非法数字/负数）就静默跳过；解析成功才写入覆盖价。
      if (parsed) setCustomPricing(key, parsed);
    }
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.usage.custom_pricing.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.usage.custom_pricing.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="max-h-[60vh] space-y-4 overflow-y-auto">
          {rows.length === 0 ? (
            <p className="py-4 text-center text-caption text-muted-foreground">
              {t(($) => $.usage.custom_pricing.empty)}
            </p>
          ) : (
            rows.map((key) => {
              const draft = drafts[key] ?? EMPTY_DRAFT;
              const hasOverride = Boolean(pricings[key]);
              return (
                <div key={key} className="space-y-2 rounded-md border p-3">
                  <div className="flex items-center justify-between gap-2">
                    <code className="truncate font-mono text-caption">{key}</code>
                    {hasOverride && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-xs"
                        onClick={() => {
                          setDrafts((current) => ({ ...current, [key]: EMPTY_DRAFT }));
                          removeCustomPricing(key);
                        }}
                        aria-label={t(($) => $.usage.custom_pricing.remove_aria)}
                        title={t(($) => $.usage.custom_pricing.remove_aria)}
                      >
                        <Trash2 />
                      </Button>
                    )}
                  </div>
                  <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                    <PriceField
                      label={t(($) => $.usage.custom_pricing.field_input)}
                      value={draft.input}
                      onChange={(v) => updateField(key, "input", v)}
                    />
                    <PriceField
                      label={t(($) => $.usage.custom_pricing.field_output)}
                      value={draft.output}
                      onChange={(v) => updateField(key, "output", v)}
                    />
                    <PriceField
                      label={t(($) => $.usage.custom_pricing.field_cache_read)}
                      value={draft.cacheRead}
                      onChange={(v) => updateField(key, "cacheRead", v)}
                    />
                    <PriceField
                      label={t(($) => $.usage.custom_pricing.field_cache_write)}
                      value={draft.cacheWrite}
                      onChange={(v) => updateField(key, "cacheWrite", v)}
                    />
                  </div>
                </div>
              );
            })
          )}
          <p className="text-micro text-muted-foreground">
            {t(($) => $.usage.custom_pricing.unit_hint)}
          </p>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t(($) => $.usage.custom_pricing.cancel)}
          </Button>
          <Button onClick={handleSave}>
            {t(($) => $.usage.custom_pricing.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PriceField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-1">
      <Label className="text-micro text-muted-foreground">{label}</Label>
      <Input
        type="number"
        inputMode="decimal"
        min="0"
        step="0.01"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="0.00"
      />
    </div>
  );
}
