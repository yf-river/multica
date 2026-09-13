// 用 SheetJS(xlsx) 把复盘导出为 .xlsx 文件：建工作簿、写行数据、加列宽与
// 超链接，最后触发浏览器下载。

import * as XLSX from "xlsx";

import type { XlsxSheetSpec } from "./run-review-export";

/**
 * 把若干张表写成 xlsx 并下载到本地。
 *
 * @param filename 下载文件名（非法字符会被替换成下划线）
 * @param sheets 各张表的内容描述（值矩阵、列宽、超链接）
 */
export function downloadRunReviewXlsx(filename: string, sheets: XlsxSheetSpec[]) {
  const workbook = XLSX.utils.book_new();
  for (const sheet of sheets) {
    const worksheet = XLSX.utils.aoa_to_sheet(sheet.rows);
    if (sheet.columnWidths?.length) {
      worksheet["!cols"] = sheet.columnWidths.map((wch) => ({ wch }));
    }
    for (const hyperlink of sheet.hyperlinks ?? []) {
      const address = XLSX.utils.encode_cell({ r: hyperlink.row, c: hyperlink.col });
      const cell = worksheet[address] ?? { t: "s", v: "" };
      cell.l = {
        Target: hyperlink.target,
        Tooltip: hyperlink.tooltip ?? hyperlink.target,
      };
      worksheet[address] = cell;
    }
    XLSX.utils.book_append_sheet(workbook, worksheet, sanitizeSheetName(sheet.name));
  }

  const data = XLSX.write(workbook, { bookType: "xlsx", type: "array" }) as ArrayBuffer;
  const blob = new Blob([data], { type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" });
  const url = window.URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename.replace(/[^\w.-]+/g, "_");
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.URL.revokeObjectURL(url);
}

// Excel 工作表名不允许 \ / : ? * [ ] 且最多 31 个字符，这里做清洗兜底。
function sanitizeSheetName(name: string) {
  return (name || "Sheet1").replace(/[\\/:?*[\]]/g, "_").slice(0, 31) || "Sheet1";
}
