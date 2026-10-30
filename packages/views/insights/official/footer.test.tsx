import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ActivityPill, UsagePill, type Translate } from "./stats";
import { ContextMeter } from "./context-meter";
import locales from "./stats-locales.json";
import contextLocales from "./context-meter-locales.json";

const stats = { turns: 2, steps: 4, llmMs: 8000, toolMs: 2200, ttftMs: 1000, ttftSteps: 2, decodeMs: 2000, decodeTokens: 300 };
const usage = { uncachedInputTokens: 10, cacheReadTokens: 99990, cacheWriteTokens: 0, outputTokens: 300 };
const translate = (dict: Record<string, string>): Translate => (key, params) => Object.entries(params ?? {}).reduce((text, [key, value]) => text.replaceAll(`{${key}}`, String(value)), dict[key] ?? key);
const mode = <T,>(selector: (value: string) => T) => selector("detailed");
afterEach(cleanup);
describe("official footer interactions", () => {
  it("opens independent timing and usage dialogs and closes with Escape or an outside click", () => {
    const projections: Record<string, unknown> = { sessionStats: stats, tokenUsage: usage };
    const props = { useProjection: (key: string) => projections[key], usePerformanceUsage: mode, t: translate(locales.zh) };
    render(<><ActivityPill {...props} /><UsagePill {...props} /></>);
    fireEvent.click(screen.getByRole("button", { name: "2 轮 4 步 · 150 tok/s" }));
    expect(screen.getByRole("dialog").textContent).toContain("0.5秒");
    fireEvent.click(screen.getByRole("button", { name: /100K tok/ }));
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(screen.getByRole("dialog").textContent).toContain("100,300 tok");
    expect(screen.getByRole("dialog").textContent).toContain("99.99%");
    expect(screen.getByRole("dialog").textContent).not.toContain("缓存写入");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /100K tok/ }));
    fireEvent.click(document.body);
    expect(screen.queryByRole("dialog")).toBeNull();
  });
  it("does not fabricate statistics or capacity when data is missing", () => {
    const props = { useProjection: (key: string) => key === "sessionStats" ? { ...stats, steps: 0 } : undefined, usePerformanceUsage: mode, t: translate(locales.en) };
    const { container } = render(<><ActivityPill {...props} /><UsagePill {...props} /><ContextMeter useProjection={props.useProjection} t={translate(contextLocales.en)} /></>);
    expect(container.textContent).toBe("");
    expect(screen.queryByRole("button")).toBeNull();
  });
  it("uses provider context pressure and opens the original composition breakdown", () => {
    const values: Record<string, unknown> = { contextPressure: { projectedTokens: 4000, pressureTokens: 3000, contextWindow: 10000 }, contextBreakdown: { systemTokens: 1000, toolsTokens: 500, messageTokens: 2500 } };
    render(<ContextMeter useProjection={(key) => values[key]} t={translate(contextLocales.en)} />);
    fireEvent.click(screen.getByRole("button", { name: "40% of context used" }));
    expect(screen.getByRole("dialog").textContent).toContain("~4K / 10K");
    expect(screen.getByRole("dialog").textContent).toContain("Tool definitions~500");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
