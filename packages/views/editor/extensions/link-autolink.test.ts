import { afterEach, describe, expect, it } from "vitest";
import { Editor } from "@tiptap/core";
import StarterKit from "@tiptap/starter-kit";
import { Markdown } from "@tiptap/markdown";
import { LinkExtension } from "./index";

let editor: Editor | null = null;

function makeEditor(): Editor {
  const element = document.createElement("div");
  document.body.appendChild(element);
  return new Editor({
    element,
    extensions: [
      StarterKit.configure({ link: false }),
      LinkExtension,
      Markdown.configure({ indentation: { style: "space", size: 3 } }),
    ],
  });
}

afterEach(() => {
  editor?.destroy();
  editor = null;
  document.body.innerHTML = "";
});

describe("LinkExtension autolink", () => {
  it("does not turn a bare email into a mailto link", () => {
    editor = makeEditor();

    editor.commands.insertContent("alice@example.com ");

    expect(editor.getMarkdown().trim()).toBe("alice@example.com");
  });

  it("still autolinks normal URLs", () => {
    editor = makeEditor();

    editor.commands.insertContent("https://example.com ");

    expect(editor.getMarkdown().trim()).toBe(
      "[https://example.com](https://example.com)",
    );
  });
});
