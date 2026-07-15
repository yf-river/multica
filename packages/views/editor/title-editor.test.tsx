import { describe, it, expect, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";

const mockFocus = vi.hoisted(() => vi.fn());
const mockSetContent = vi.hoisted(() => vi.fn());
const mockBlur = vi.hoisted(() => vi.fn());
const editorState = vi.hoisted(() => ({
  isFocused: false,
  isDestroyed: false,
  text: "",
}));

vi.mock("../i18n", () => ({
  useT: () => ({ t: (fn: unknown) => (typeof fn === "function" ? "" : "") }),
}));

const editorRef = vi.hoisted<{ current: unknown }>(() => ({ current: null }));

vi.mock("@tiptap/react", () => ({
  useEditor: () => {
    if (!editorRef.current) {
      editorRef.current = {
        get isFocused() {
          return editorState.isFocused;
        },
        get isDestroyed() {
          return editorState.isDestroyed;
        },
        commands: {
          focus: mockFocus,
          blur: mockBlur,
          setContent: mockSetContent,
        },
        getText: () => editorState.text,
      };
    }
    return editorRef.current;
  },
  EditorContent: () => <div data-testid="editor-content" />,
}));

import { TitleEditor } from "./title-editor";

function renderTitle(value: string) {
  editorState.text = value;
  return render(<TitleEditor defaultValue={value} />);
}

function expectTitleContent(value: string) {
  expect(mockSetContent).toHaveBeenCalledTimes(1);
  expect(mockSetContent).toHaveBeenCalledWith(
    value
      ? {
          type: "doc",
          content: [
            {
              type: "paragraph",
              content: [{ type: "text", text: value }],
            },
          ],
        }
      : "",
    { emitUpdate: false },
  );
}

describe("TitleEditor", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    editorState.isFocused = false;
    editorState.isDestroyed = false;
    editorState.text = "";
    editorRef.current = null;
  });

  it("syncs editor content when defaultValue changes externally and editor is unfocused", () => {
    const { rerender } = renderTitle("old title");

    expect(mockSetContent).not.toHaveBeenCalled();

    rerender(<TitleEditor defaultValue="new title from server" />);

    expectTitleContent("new title from server");
  });

  it("does not overwrite the user's in-flight edits when the editor is focused and dirty", () => {
    const { rerender } = renderTitle("old title");

    editorState.isFocused = true;
    editorState.text = "user typed but not yet blurred";

    rerender(<TitleEditor defaultValue="external update" />);

    expect(mockSetContent).not.toHaveBeenCalled();
  });

  // Regression: a focused but clean editor (user clicked in but never typed)
  // must still accept external updates, otherwise the subsequent blur would
  // compare stale editor text to the new server value and silently roll the
  // external update back.
  it("syncs to new defaultValue when editor is focused but clean", () => {
    const { rerender } = renderTitle("old title");

    // User clicked into the title field but has not typed anything yet:
    // editor text still equals the previous defaultValue.
    editorState.isFocused = true;
    editorState.text = "old title";

    rerender(<TitleEditor defaultValue="new title from server" />);

    expectTitleContent("new title from server");
  });

  it("short-circuits when editor text already equals incoming defaultValue", () => {
    const { rerender } = renderTitle("same title");

    // Force the effect to re-run by rendering with a different prop, then
    // back to the same value. Even an identity-equal prop should be skipped.
    rerender(<TitleEditor defaultValue="same title" />);

    expect(mockSetContent).not.toHaveBeenCalled();
  });

  it("clears the editor when defaultValue transitions to empty", () => {
    const { rerender } = renderTitle("old title");

    rerender(<TitleEditor defaultValue="" />);

    expectTitleContent("");
  });
});
