import { act, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useIssueDetailScrollRestore } from "./use-issue-detail-scroll-restore";

it("opens each ready task at the latest content and releases control on user scroll", () => {
  let resize: () => void = () => {};
  const disconnect = vi.fn();
  vi.stubGlobal("ResizeObserver", class { constructor(callback: () => void) { resize = callback; } observe() {} disconnect = disconnect; });
  const el = document.createElement("div");
  el.append(document.createElement("div"));
  Object.defineProperty(el, "scrollHeight", { value: 2000, configurable: true });
  function Harness({ id, ready = true, disabled = false }: { id: string; ready?: boolean; disabled?: boolean }) {
    useIssueDetailScrollRestore({ restoreKey: id, scrollContainerEl: el, ready, disabled, startAtBottom: true });
    return null;
  }
  const view = render(<Harness id="a" ready={false} />);
  expect(el.scrollTop).toBe(0);
  view.rerender(<Harness id="a" />);
  expect(el.scrollTop).toBe(2000);
  Object.defineProperty(el, "scrollHeight", { value: 3000 });
  act(resize);
  expect(el.scrollTop).toBe(3000);
  fireEvent.wheel(el);
  expect(disconnect).toHaveBeenCalled();
  el.scrollTop = 50;
  view.rerender(<Harness id="b" />);
  expect(el.scrollTop).toBe(3000);
  el.scrollTop = 400;
  view.rerender(<Harness id="deep-link" disabled />);
  expect(el.scrollTop).toBe(400);
  view.unmount();
});
afterEach(() => vi.unstubAllGlobals());
