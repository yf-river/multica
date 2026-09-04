import * as XLSX from "xlsx";

import type { XlsxSheetSpec } from "./run-review-export";

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

function sanitizeSheetName(name: string) {
  return (name || "Sheet1").replace(/[\\/:?*[\]]/g, "_").slice(0, 31) || "Sheet1";
}
