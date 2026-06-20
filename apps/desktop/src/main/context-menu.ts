import {
  BrowserWindow,
  Menu,
  MenuItem,
  clipboard,
  type WebContents,
} from "electron";
import { isSafeExternalHttpUrl, openExternalSafely } from "./external-url";

// Electron ships with no default right-click menu, so a user selecting text
// in the renderer has no way to copy it. Mirror Chrome's minimal clipboard
// menu using `roles`; custom link items use fixed Chinese labels.
export function installContextMenu(webContents: WebContents): void {
  webContents.on("context-menu", (_event, params) => {
    const { editFlags, selectionText, isEditable, linkURL } = params;
    const hasSelection = selectionText.trim().length > 0;
    // params.linkURL is the resolved absolute URL of the anchor under the
    // cursor; Electron normalizes relative hrefs against the page URL for
    // us, so we only need to gate on the http(s) scheme allowlist
    // (mirrors openExternalSafely + the renderer's <a> usage). Empty for
    // non-link right-clicks; other schemes (mailto:, javascript:, custom
    // app schemes) are intentionally not surfaced — opening them via
    // shell.openExternal would route through the OS handler and is
    // outside what this menu promises.
    const linkIsHttpUrl = !!linkURL && isSafeExternalHttpUrl(linkURL);
    const labels = pickLabels();

    const menu = new Menu();

    if (isEditable && editFlags.canCut) {
      menu.append(new MenuItem({ role: "cut" }));
    }
    if (hasSelection && editFlags.canCopy) {
      menu.append(new MenuItem({ role: "copy" }));
    }
    if (isEditable && editFlags.canPaste) {
      menu.append(new MenuItem({ role: "paste" }));
    }
    if (isEditable && editFlags.canSelectAll) {
      if (menu.items.length > 0) {
        menu.append(new MenuItem({ type: "separator" }));
      }
      menu.append(new MenuItem({ role: "selectAll" }));
    }

    // Link items — only when the cursor is over an actual http(s) <a>.
    // Without these the renderer's <a target="_blank"> gives users no
    // standard right-click affordance ("Open in new window", "Copy link
    // address"); the default click handler does forward to
    // setWindowOpenHandler → openExternalSafely, but discoverability via
    // the keyboard / mouse context menu was missing.
    if (linkIsHttpUrl) {
      if (menu.items.length > 0) {
        menu.append(new MenuItem({ type: "separator" }));
      }
      menu.append(
        new MenuItem({
          label: labels.openLink,
          click: () => {
            // openExternalSafely re-validates the scheme — defense in
            // depth in case Electron ever surfaces a non-http linkURL
            // we forgot to filter at this layer.
            void openExternalSafely(linkURL);
          },
        }),
      );
      menu.append(
        new MenuItem({
          label: labels.copyLinkAddress,
          click: () => {
            clipboard.writeText(linkURL);
          },
        }),
      );
    }

    if (menu.items.length === 0) return;
    const window = BrowserWindow.fromWebContents(webContents) ?? undefined;
    menu.popup({ window });
  });
}

type ContextMenuLabels = {
  openLink: string;
  copyLinkAddress: string;
};

const contextMenuLabels: ContextMenuLabels = {
  openLink: "在浏览器中打开链接",
  copyLinkAddress: "复制链接地址",
};

function pickLabels(): ContextMenuLabels {
  return contextMenuLabels;
}
