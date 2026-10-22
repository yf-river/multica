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


describe("comment file attachment", () => {
  it("names the file and directory, hiding zero statistics", () => {
    render(<CodeDiffCard variant="attachment" result={{ code_diff: { ...result.code_diff, files: [{ ...result.code_diff.files[0], path: "src/notes.ts", deleted: 0 }] } }} />);
    const card = screen.getByRole("button", { name: "查看文件修改：notes.ts" });
    expect(card).toHaveTextContent("src/");
    expect(card).toHaveTextContent("+1");
    expect(card).not.toHaveTextContent("−0");
    fireEvent.click(card);
    expect(screen.getByRole("dialog", { name: "文件修改" })).toBeTruthy();
  });
  it("summarizes multiple files and discloses partial results", () => {
    render(<CodeDiffCard variant="attachment" result={{ code_diff: { ...result.code_diff, truncated: true, files: [...result.code_diff.files, { ...result.code_diff.files[0], path: "README.md" }] } }} />);
    const card = screen.getByRole("button", { name: /3 个文件已修改.*部分/ });
    expect(card).toHaveTextContent("app.ts、image.png 等");
    expect(card).toHaveTextContent("+2");
    expect(card).toHaveTextContent("−2");
  });
  it("identifies a binary file without showing empty line counts", () => {
    render(<CodeDiffCard variant="attachment" result={{ code_diff: { ...result.code_diff, files: [result.code_diff.files[1]] } }} />);
    const card = screen.getByRole("button", { name: /image.png/ });
    expect(card).toHaveTextContent("二进制文件");
    expect(card).not.toHaveTextContent("+0");
    expect(card).not.toHaveTextContent("−0");
  });
});
