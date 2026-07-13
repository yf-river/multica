import { describe, expect, it } from "vitest";
import {
  formatEventLabel,
  formatFilterLabel,
  formatToolName,
  localizeTranscriptOutput,
  summarizeToolInput,
  transcriptTruncatedSuffix,
  truncateTranscriptText,
} from "./format";

describe("task transcript display formatting", () => {
  it("localizes common tool names while preserving unknown tools", () => {
    expect(formatToolName("Bash")).toBe("终端");
    expect(formatToolName("exec_command")).toBe("终端");
    expect(formatToolName("TaskCreate")).toBe("创建待办");
    expect(formatToolName("TaskUpdate")).toBe("更新待办");
    expect(formatToolName("CustomTool")).toBe("CustomTool");
  });

  it("localizes event and filter labels", () => {
    expect(formatEventLabel({ seq: 1, type: "text" })).toBe("智能体");
    expect(formatEventLabel({ seq: 2, type: "thinking" })).toBe("思考");
    expect(formatEventLabel({ seq: 3, type: "error" })).toBe("错误");
    expect(formatEventLabel({ seq: 4, type: "tool_use", tool: "Bash" })).toBe("终端");
    expect(formatFilterLabel({ seq: 5, type: "tool_result", tool: "TaskUpdate" })).toBe("工具：更新待办");
  });

  it("localizes fixed tool output labels without changing command content", () => {
    const output = [
      "Command: multica issue get MUL-1 --output json",
      "Stdout: {}",
      "Stderr: (empty)",
      "Exit Code: 0",
      "Signal: (none)",
    ].join("\n");

    expect(localizeTranscriptOutput(output)).toBe([
      "命令：multica issue get MUL-1 --output json",
      "标准输出：{}",
      "标准错误：（空）",
      "退出码：0",
      "信号：(none)",
    ].join("\n"));
  });

  it("uses Chinese truncation text", () => {
    expect(truncateTranscriptText("abcdef", 3)).toBe("abc...");
    expect(transcriptTruncatedSuffix()).toBe("\n...（已截断）");
    expect(localizeTranscriptOutput("... (truncated)")).toBe("...（已截断）");
  });

  it("uses one tool-input summary rule with caller-selected truncation", () => {
    expect(summarizeToolInput({ file_path: "/repo/src/file.ts" }, 10)).toBe(".../src/file.ts");
    expect(summarizeToolInput({ command: "123456", prompt: "ignored" }, 4)).toBe("1234...");
    expect(summarizeToolInput({ unknown: "fallback" }, 10)).toBe("fallback");
  });
});
