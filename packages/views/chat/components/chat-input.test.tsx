import { forwardRef, useRef, useImperativeHandle } from "react";
import { beforeEach, describe, it, expect, vi } from "vitest";
import { act, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Attachment } from "@multica/core/types";
import enCommon from "../../locales/zh-Hans/common.json";
import enChat from "../../locales/zh-Hans/chat.json";

function makeUpload(overrides: Partial<Attachment> & { id: string; url: string; filename: string }): Attachment {
  return {
    content_type: "image/png",
    size_bytes: 1,
    ...overrides,
    url: overrides.url,
    download_url: overrides.download_url ?? overrides.url,
    markdown_url: overrides.markdown_url ?? overrides.url,
  };
}

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, chat: enChat } };

const dropHandlers = vi.hoisted(() => ({
  onDrop: null as null | ((files: File[]) => void),
}));
const editorProps = vi.hoisted(() => ({
  last: null as null | Record<string, unknown>,
}));
const editorState = vi.hoisted(() => ({ cleared: 0, blurred: 0 }));

vi.mock("../../editor", () => ({
  useFileDropZone: ({ onDrop }: { onDrop: (files: File[]) => void }) => {
    dropHandlers.onDrop = onDrop;
    return { isDragOver: false, dropZoneProps: { "data-testid": "drop-zone" } };
  },
  FileDropOverlay: () => null,
  ContentEditor: forwardRef(function MockContentEditor(
    props: {
      defaultValue?: string;
      onUpdate?: (md: string) => void;
      placeholder?: string;
      onUploadFile?: (file: File) => Promise<Attachment | null>;
      mentionMode?: string;
      mentionContextItems?: unknown[];
    },
    ref: React.Ref<unknown>,
  ) {
    const {
      defaultValue,
      onUpdate,
      placeholder,
      onUploadFile,
    } = props;
    editorProps.last = props as unknown as Record<string, unknown>;
    const valueRef = useRef<string>(defaultValue ?? "");
    const uploadingRef = useRef(0);
    useImperativeHandle(ref, () => ({
      getMarkdown: () => valueRef.current,
      clearContent: () => {
        editorState.cleared += 1;
        valueRef.current = "";
      },
      blur: () => {
        editorState.blurred += 1;
      },
      focus: () => {},
      uploadFile: async (file: File) => {
        uploadingRef.current += 1;
        try {
          const result = await onUploadFile?.(file);
          if (result) {
            const persistedURL = result.markdown_url;
            valueRef.current = `${valueRef.current}![](${persistedURL})`.trim();
            onUpdate?.(valueRef.current);
          }
        } finally {
          uploadingRef.current = Math.max(0, uploadingRef.current - 1);
        }
      },
      hasActiveUploads: () => uploadingRef.current > 0,
    }));
    return (
      <textarea
        data-testid="editor"
        placeholder={placeholder}
        onChange={(e) => {
          valueRef.current = e.target.value;
          onUpdate?.(e.target.value);
        }}
      />
    );
  }),
}));

vi.mock("@multica/core/chat", () => {
  const state = {
    activeSessionId: null as string | null,
    selectedAgentId: "agent-1",
    inputDrafts: {} as Record<string, string>,
    inputDraftAttachments: {} as Record<string, Attachment[]>,
    setInputDraft: vi.fn(),
    setInputDraftAttachments: vi.fn(),
    addInputDraftAttachment: vi.fn(),
    clearInputDraft: vi.fn(),
    clearInputDraftForWorkspace: vi.fn(),
  };
  return {
    newSessionDraftKey: (agentId: string | null) => `__draft_new__:${agentId ?? ""}`,
    useChatStore: Object.assign(
      (selector?: (s: typeof state) => unknown) =>
        selector ? selector(state) : state,
      { getState: () => state },
    ),
  };
});

import { ChatInput } from "./chat-input";
import { useChatStore } from "@multica/core/chat";
import { setCurrentWorkspace } from "@multica/core/platform";

type ChatInputOnSend = React.ComponentProps<typeof ChatInput>["onSend"];
type ChatInputCommit = Parameters<ChatInputOnSend>[2];

beforeEach(() => {
  dropHandlers.onDrop = null;
  editorProps.last = null;
  editorState.cleared = 0;
  editorState.blurred = 0;
  const state = useChatStore.getState() as unknown as {
    activeSessionId: string | null;
    selectedAgentId: string;
    inputDrafts: Record<string, string>;
    setInputDraft: ReturnType<typeof vi.fn>;
    clearInputDraft: ReturnType<typeof vi.fn>;
    clearInputDraftForWorkspace: ReturnType<typeof vi.fn>;
    inputDraftAttachments: Record<string, Attachment[]>;
    setInputDraftAttachments: ReturnType<typeof vi.fn>;
    addInputDraftAttachment: ReturnType<typeof vi.fn>;
  };
  state.activeSessionId = null;
  state.selectedAgentId = "agent-1";
  state.inputDrafts = {};
  state.inputDraftAttachments = {};
  state.setInputDraft.mockClear();
  state.setInputDraft.mockImplementation((key: string, value: string) => {
    state.inputDrafts[key] = value;
  });
  state.setInputDraftAttachments.mockClear();
  state.setInputDraftAttachments.mockImplementation((key: string, attachments: Attachment[]) => {
    if (attachments.length > 0) state.inputDraftAttachments[key] = attachments;
    else delete state.inputDraftAttachments[key];
  });
  state.addInputDraftAttachment.mockClear();
  state.addInputDraftAttachment.mockImplementation((key: string, attachment: Attachment) => {
    const existing = state.inputDraftAttachments[key] ?? [];
    state.inputDraftAttachments[key] = existing.some((a) => a.id === attachment.id)
      ? existing.map((a) => (a.id === attachment.id ? attachment : a))
      : [...existing, attachment];
  });
  state.clearInputDraft.mockClear();
  state.clearInputDraft.mockImplementation((key: string) => {
    delete state.inputDrafts[key];
    delete state.inputDraftAttachments[key];
  });
  state.clearInputDraftForWorkspace.mockClear();
  setCurrentWorkspace(null, null);
});

function renderInput(props: Partial<React.ComponentProps<typeof ChatInput>> = {}) {
  const onSend = props.onSend ?? vi.fn();
  const onUploadFile =
    props.onUploadFile ??
    vi.fn(async (_file: File) =>
      makeUpload({ id: "att-1", url: "https://cdn.example/att-1.png", filename: "img.png" }),
    );
  render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <ChatInput onSend={onSend} onUploadFile={onUploadFile} agentName="Multica" {...props} />
    </I18nProvider>,
  );
  return { onSend, onUploadFile };
}

async function dropFile(
  file: File,
  settle: "upload-started" | "upload-finished" = "upload-finished",
) {
  await act(async () => {
    dropHandlers.onDrop?.([file]);
    await Promise.resolve();
    if (settle === "upload-finished") await Promise.resolve();
  });
}

function getSendButton() {
  const buttons = screen.getAllByRole("button");
  return buttons[buttons.length - 1]!;
}

async function waitForSendButton(state: "enabled" | "disabled") {
  let sendButton: HTMLElement;
  await waitFor(() => {
    sendButton = getSendButton();
    if (state === "enabled") expect(sendButton).not.toBeDisabled();
    else expect(sendButton).toBeDisabled();
  });
  return sendButton!;
}

async function clickEnabledSendButton() {
  const button = await waitForSendButton("enabled");
  await act(async () => {
    fireEvent.click(button);
  });
}

async function typeDraft(content: string) {
  fireEvent.change(screen.getByTestId("editor"), { target: { value: content } });
  return waitForSendButton("enabled");
}

describe("ChatInput @ context wiring", () => {
  it("configures chat @ with current/recent issue/project context", () => {
    const contextItems = [
      { id: "issue-1", label: "MUL-1", type: "issue" as const, group: "current" as const },
    ];

    renderInput({ contextItems });

    expect(editorProps.last?.mentionMode).toBe("context");
    expect(editorProps.last?.mentionContextItems).toBe(contextItems);
  });
});

describe("ChatInput attachment wiring", () => {
  it("routes dropped files through the editor's upload handler", async () => {
    const { onUploadFile } = renderInput();
    expect(dropHandlers.onDrop).not.toBeNull();
    const file = new File(["x"], "drop.png", { type: "image/png" });
    await dropFile(file);
    expect(onUploadFile).toHaveBeenCalledWith(file);
  });

  it("passes attachment_ids to onSend for uploads still referenced in the content", async () => {
    const onSend = vi.fn();
    const onUploadFile = vi.fn(async (_file: File) =>
      makeUpload({ id: "att-42", url: "https://cdn.example/att-42.png", filename: "x.png" }),
    );
    renderInput({ onSend, onUploadFile });

    const file = new File(["x"], "drop.png", { type: "image/png" });
    await dropFile(file);

    await clickEnabledSendButton();

    expect(onSend).toHaveBeenCalledTimes(1);
    const [, ids] = onSend.mock.calls[0]!;
    expect(ids).toEqual(["att-42"]);
    expect(useChatStore.getState().addInputDraftAttachment).toHaveBeenCalledWith(
      "__draft_new__:agent-1",
      expect.objectContaining({ id: "att-42" }),
    );
  });

  it("binds attachment_ids when markdown_url differs from the storage URL", async () => {
    const onSend = vi.fn();
    const SHORT_LIVED_LINK = "/uploads/workspaces/ws-1/foo.png?exp=42&sig=stale";
    const STABLE_MARKDOWN_LINK = "/api/attachments/att-99/download";
    const onUploadFile = vi.fn(async (_file: File) =>
      makeUpload({
        id: "att-99",
        url: SHORT_LIVED_LINK,
        markdown_url: STABLE_MARKDOWN_LINK,
        filename: "foo.png",
      }),
    );
    renderInput({ onSend, onUploadFile });

    const file = new File(["x"], "foo.png", { type: "image/png" });
    await dropFile(file);

    await clickEnabledSendButton();

    expect(onSend).toHaveBeenCalledTimes(1);
    const [content, ids] = onSend.mock.calls[0]!;
    expect(content).toContain(STABLE_MARKDOWN_LINK);
    expect(content).not.toContain("?exp=");
    expect(content).not.toContain("?sig=");
    expect(ids).toEqual(["att-99"]);
  });

  it("disables send while an upload is in flight, re-enables after it resolves", async () => {
    let resolveUpload: (v: Attachment) => void;
    const uploadPromise = new Promise<Attachment>((res) => {
      resolveUpload = res;
    });
    const onSend = vi.fn();
    const onUploadFile = vi.fn(() => uploadPromise);
    renderInput({ onSend, onUploadFile });

    fireEvent.change(screen.getByTestId("editor"), { target: { value: "preview text" } });

    const file = new File(["x"], "slow.png", { type: "image/png" });
    await dropFile(file, "upload-started");

    await waitForSendButton("disabled");

    await act(async () => {
      resolveUpload!(makeUpload({ id: "att-slow", url: "https://cdn.example/att-slow.png", filename: "slow.png" }));
      await Promise.resolve();
    });

    await clickEnabledSendButton();
    expect(onSend).toHaveBeenCalledTimes(1);
    const [, ids] = onSend.mock.calls[0]!;
    expect(ids).toEqual(["att-slow"]);
  });

  it("does not render the file upload button when onUploadFile is omitted", () => {
    renderInput({ onUploadFile: undefined });
    const buttons = screen.getAllByRole("button");
    expect(buttons.length).toBe(1);
  });
});

describe("ChatInput async send", () => {
  it("restores a cancelled empty run draft into the editor", async () => {
    const state = useChatStore.getState() as unknown as { activeSessionId: string | null };
    state.activeSessionId = "session-a";
    const onRestoreDraftConsumed = vi.fn();
    renderInput({
      restoreDraftRequest: {
        id: "msg-restored",
        content: "bring this back",
        sessionId: "session-a",
      },
      onRestoreDraftConsumed,
    });

    await waitFor(() => {
      expect(useChatStore.getState().setInputDraft).toHaveBeenCalledWith(
        "session-a",
        "bring this back",
      );
      expect(editorProps.last?.defaultValue).toBe("bring this back");
      expect(onRestoreDraftConsumed).toHaveBeenCalledTimes(1);
    });
  });

  it("consumes a restore request even when an existing draft blocks restore", async () => {
    const state = useChatStore.getState() as unknown as {
      activeSessionId: string | null;
      inputDrafts: Record<string, string>;
      setInputDraft: ReturnType<typeof vi.fn>;
    };
    state.activeSessionId = "session-a";
    state.inputDrafts["session-a"] = "already typing";
    const onRestoreDraftConsumed = vi.fn();

    renderInput({
      restoreDraftRequest: {
        id: "msg-restored",
        content: "bring this back",
        sessionId: "session-a",
      },
      onRestoreDraftConsumed,
    });

    await waitFor(() => {
      expect(onRestoreDraftConsumed).toHaveBeenCalledTimes(1);
    });
    expect(state.setInputDraft).not.toHaveBeenCalledWith(
      "session-a",
      "bring this back",
    );
  });

  it("keeps the draft while send is pending until the owner commits the handoff", async () => {
    let resolveSend: (accepted: boolean) => void;
    const sendPromise = new Promise<boolean>((res) => {
      resolveSend = res;
    });
    const onSend = vi.fn<ChatInputOnSend>(() => sendPromise);
    renderInput({ onSend });

    const sendButton = await typeDraft("slow network");
    fireEvent.click(sendButton);

    expect(onSend).toHaveBeenCalledWith(
      "slow network",
      undefined,
      expect.any(Function),
      [],
    );
    expect(useChatStore.getState().clearInputDraft).not.toHaveBeenCalled();
    await waitFor(() => expect(sendButton).toBeDisabled());

    const commitInput = onSend.mock.calls[0]![2] as ChatInputCommit;
    act(() => {
      commitInput({ extraDraftKeys: ["session-1"] });
    });

    expect(useChatStore.getState().clearInputDraft).toHaveBeenCalledWith("__draft_new__:agent-1");
    expect(useChatStore.getState().clearInputDraft).toHaveBeenCalledWith("session-1");

    await act(async () => {
      resolveSend!(true);
      await sendPromise;
    });

    expect(useChatStore.getState().clearInputDraft).toHaveBeenCalledTimes(2);
  });

  it("keeps the draft when send is rejected by the owner", async () => {
    const onSend = vi.fn(async () => false);
    renderInput({ onSend });

    const sendButton = await typeDraft("retry me");

    await act(async () => {
      fireEvent.click(sendButton);
      await Promise.resolve();
    });

    expect(onSend).toHaveBeenCalledWith("retry me", undefined, expect.any(Function), []);
    expect(useChatStore.getState().clearInputDraft).not.toHaveBeenCalled();
  });

  it("sends attachment ids restored from persisted draft attachments", async () => {
    const state = useChatStore.getState() as unknown as {
      inputDrafts: Record<string, string>;
      inputDraftAttachments: Record<string, Attachment[]>;
    };
    const attachment = makeUpload({
      id: "att-persisted",
      url: "/api/attachments/att-persisted/download",
      filename: "persisted.png",
    });
    state.inputDrafts["__draft_new__:agent-1"] = "see ![](/api/attachments/att-persisted/download)";
    state.inputDraftAttachments["__draft_new__:agent-1"] = [attachment];

    const onSend = vi.fn<ChatInputOnSend>((_content, _ids, commitInput) => {
      commitInput();
      return true;
    });
    renderInput({ onSend });

    fireEvent.click(await waitForSendButton("enabled"));

    expect(onSend).toHaveBeenCalledWith(
      "see ![](/api/attachments/att-persisted/download)",
      ["att-persisted"],
      expect.any(Function),
      [attachment],
    );
  });
});

describe("ChatInput session-aware restore", () => {
  function element(props: Partial<React.ComponentProps<typeof ChatInput>>) {
    return (
      <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
        <ChatInput onSend={vi.fn()} onUploadFile={vi.fn()} agentName="Multica" {...props} />
      </I18nProvider>
    );
  }

  it("holds a session-scoped restore until the user returns to the source session", async () => {
    const state = useChatStore.getState() as unknown as {
      activeSessionId: string | null;
      setInputDraft: ReturnType<typeof vi.fn>;
    };
    state.activeSessionId = "session-b";
    const onRestoreDraftConsumed = vi.fn();
    const props = {
      restoreDraftRequest: { id: "r1", content: "from A", sessionId: "session-a" },
      onRestoreDraftConsumed,
    };
    const { rerender } = render(element(props));

    expect(onRestoreDraftConsumed).not.toHaveBeenCalled();
    expect(state.setInputDraft).not.toHaveBeenCalledWith("session-b", "from A");

    state.activeSessionId = "session-a";
    rerender(element(props));

    await waitFor(() => {
      expect(state.setInputDraft).toHaveBeenCalledWith("session-a", "from A");
      expect(onRestoreDraftConsumed).toHaveBeenCalledTimes(1);
    });
  });

  it("consumes a session-scoped restore when already on that session", async () => {
    const state = useChatStore.getState() as unknown as {
      activeSessionId: string | null;
      setInputDraft: ReturnType<typeof vi.fn>;
    };
    state.activeSessionId = "session-a";
    const onRestoreDraftConsumed = vi.fn();
    render(
      element({
        restoreDraftRequest: { id: "r2", content: "hi A", sessionId: "session-a" },
        onRestoreDraftConsumed,
      }),
    );

    await waitFor(() => {
      expect(state.setInputDraft).toHaveBeenCalledWith("session-a", "hi A");
      expect(onRestoreDraftConsumed).toHaveBeenCalledTimes(1);
    });
  });
});

describe("ChatInput commit handoff", () => {
  async function typeAndSend(onSend: ChatInputOnSend) {
    renderInput({ onSend });
    fireEvent.click(await typeDraft("msg"));
    await waitFor(() => expect(onSend).toHaveBeenCalled());
  }

  it("scrubs the editor and clears the draft on a normal commit", async () => {
    const onSend = vi.fn<ChatInputOnSend>((_content, _ids, commitInput) => {
      commitInput();
      return true;
    });
    await typeAndSend(onSend);

    expect(editorState.cleared).toBeGreaterThan(0);
    expect(editorState.blurred).toBeGreaterThan(0);
    expect(useChatStore.getState().clearInputDraft).toHaveBeenCalledWith("__draft_new__:agent-1");
  });

  it("leaves the editor intact on a fire-and-forget commit but still clears the sent draft", async () => {
    const onSend = vi.fn<ChatInputOnSend>((_content, _ids, commitInput) => {
      commitInput({ clearEditor: false });
      return true;
    });
    await typeAndSend(onSend);

    expect(editorState.cleared).toBe(0);
    expect(editorState.blurred).toBe(0);
    expect(useChatStore.getState().clearInputDraft).toHaveBeenCalledWith("__draft_new__:agent-1");
  });

  it("clears the workspace captured at send even if navigation changes before commit", async () => {
    setCurrentWorkspace("team-a", "ws-a");
    const onSend = vi.fn<ChatInputOnSend>((_content, _ids, commitInput) => {
      setCurrentWorkspace("team-b", "ws-b");
      commitInput({ clearEditor: false });
      return true;
    });

    await typeAndSend(onSend);

    expect(useChatStore.getState().clearInputDraftForWorkspace).toHaveBeenCalledWith(
      "team-a",
      "__draft_new__:agent-1",
    );
    expect(useChatStore.getState().clearInputDraft).not.toHaveBeenCalled();
  });
});
