export type DatasetCaseSourceFilter = "全部" | "手工" | "trace导入" | "资产载荷";

export function caseSourceLabel(source: string): Exclude<DatasetCaseSourceFilter, "全部"> | "导入" {
  if (source === "manual") return "手工";
  if (source === "trace") return "trace导入";
  if (source === "payload") return "资产载荷";
  return "导入";
}
