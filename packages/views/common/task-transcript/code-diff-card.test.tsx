import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CodeDiffCard } from "./code-diff-card";

const result = { code_diff: { before: "123456789012345", after: "987654321098765", truncated: false, unavailable: false, files: [
  { path: "app.ts", added: 1, deleted: 1, binary: false, truncated: false, patch: "@@ -1 +1 @@\n-old\n+new" },
  { path: "image.png", added: 0, deleted: 0, binary: true, truncated: false, patch: "" },
] } };
describe("CodeDiffCard", () => {
  it("opens a snapshot, switches files, and closes with Escape", () => {
    render(<CodeDiffCard result={result} />);
    fireEvent.click(screen.getByRole("button", { name: /查看本次代码修改/ }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(screen.getByText("+new")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /image.png/ }));
    expect(screen.getByText("二进制文件，无法显示文本差异。")).toBeTruthy();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
  });
  it("hides empty and malformed results", () => {
    const { container, rerender } = render(<CodeDiffCard result={null} />);
    expect(container.textContent).toBe("");
    rerender(<CodeDiffCard result={{ code_diff: { ...result.code_diff, files: [] } }} />);
    expect(container.textContent).toBe("");
  });
  it("shows truncation and capture failure explicitly", () => {
    const { rerender } = render(<CodeDiffCard result={{ code_diff: { ...result.code_diff, truncated: true } }} />);
    expect(screen.getByRole("button", { name: /部分/ })).toBeTruthy();
    rerender(<CodeDiffCard result={{ code_diff: { ...result.code_diff, unavailable: true } }} />);
    expect(screen.getByText(/采集失败/)).toBeTruthy();
  });
});
