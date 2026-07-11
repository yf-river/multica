import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  __FRAGMENT_NAV_SHIM__,
  withFragmentNavShim,
} from "./iframe-fragment-nav";

describe("withFragmentNavShim", () => {
  it("appends the shim verbatim at the end of the original HTML", () => {
    const html = "<h1 id='a'>A</h1><a href='#a'>jump</a>";
    const out = withFragmentNavShim(html);
    expect(out.startsWith(html)).toBe(true);
    expect(out.endsWith(__FRAGMENT_NAV_SHIM__)).toBe(true);
    expect(out).toBe(html + __FRAGMENT_NAV_SHIM__);
  });

  it("does not mutate the input string", () => {
    const html = "<p>hi</p>";
    withFragmentNavShim(html);
    expect(html).toBe("<p>hi</p>");
  });

  it("handles empty input", () => {
    expect(withFragmentNavShim("")).toBe(__FRAGMENT_NAV_SHIM__);
  });
});

// The shim itself ships as a <script> string injected into a srcdoc iframe.
// To exercise its runtime behavior in unit tests, evaluate the inner script
// against the current document — jsdom's environment matches what runs inside
// the iframe closely enough for the click-handling contract.
//
// scrollIntoView is not implemented in jsdom; we stub it per-test.
function loadShimIntoDocument(targetDocument: Document) {
  const inner = __FRAGMENT_NAV_SHIM__
    .replace(/^<script>/, "")
    .replace(/<\/script>$/, "");
  new Function("document", inner)(targetDocument);
}

describe("fragment-nav shim runtime behavior", () => {
  let scrollSpy: ReturnType<typeof vi.fn>;
  let testDocument: Document;

  beforeEach(() => {
    // A detached document has DOM event semantics but no browsing context,
    // so links that the shim intentionally ignores cannot ask jsdom to perform
    // its unimplemented top-level navigation. It also gives every test one
    // listener set instead of accumulating handlers on the global document.
    testDocument = document.implementation.createHTMLDocument("fragment-nav-test");
    scrollSpy = vi.fn();
    // Patch the prototype so any element we create inherits the stub.
    Object.defineProperty(window.Element.prototype, "scrollIntoView", {
      configurable: true,
      writable: true,
      value: scrollSpy,
    });
    loadShimIntoDocument(testDocument);
  });

  it("scrolls the matching target into view when a fragment link is clicked", () => {
    const section = testDocument.createElement("section");
    section.id = "intro";
    section.textContent = "intro";
    testDocument.body.appendChild(section);

    const link = testDocument.createElement("a");
    link.setAttribute("href", "#intro");
    link.textContent = "go";
    testDocument.body.appendChild(link);

    const evt = new MouseEvent("click", { bubbles: true, cancelable: true });
    link.dispatchEvent(evt);

    expect(scrollSpy).toHaveBeenCalled();
    expect(scrollSpy.mock.instances[0]).toBe(section);
    expect(evt.defaultPrevented).toBe(true);
  });

  it("falls back to <a name='...'> when no element id matches", () => {
    const target = testDocument.createElement("a");
    target.setAttribute("name", "legacy");
    testDocument.body.appendChild(target);

    const link = testDocument.createElement("a");
    link.setAttribute("href", "#legacy");
    testDocument.body.appendChild(link);

    const evt = new MouseEvent("click", { bubbles: true, cancelable: true });
    link.dispatchEvent(evt);

    expect(scrollSpy).toHaveBeenCalled();
    expect(scrollSpy.mock.instances[0]).toBe(target);
    expect(evt.defaultPrevented).toBe(true);
  });

  it("decodes percent-encoded fragment ids", () => {
    const section = testDocument.createElement("section");
    section.id = "中文";
    testDocument.body.appendChild(section);

    const link = testDocument.createElement("a");
    link.setAttribute("href", `#${encodeURIComponent("中文")}`);
    testDocument.body.appendChild(link);

    link.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));

    expect(scrollSpy).toHaveBeenCalled();
    expect(scrollSpy.mock.instances[0]).toBe(section);
  });

  it("does not intercept when the click target is not inside an anchor", () => {
    const div = testDocument.createElement("div");
    div.textContent = "not a link";
    testDocument.body.appendChild(div);

    const evt = new MouseEvent("click", { bubbles: true, cancelable: true });
    div.dispatchEvent(evt);

    expect(scrollSpy).not.toHaveBeenCalled();
    expect(evt.defaultPrevented).toBe(false);
  });

  it("does not intercept links to external URLs", () => {
    const link = testDocument.createElement("a");
    link.setAttribute("href", "https://example.com/page#section");
    testDocument.body.appendChild(link);

    const evt = new MouseEvent("click", { bubbles: true, cancelable: true });
    link.dispatchEvent(evt);

    expect(scrollSpy).not.toHaveBeenCalled();
    expect(evt.defaultPrevented).toBe(false);
  });

  it("does not intercept bare '#' links", () => {
    const link = testDocument.createElement("a");
    link.setAttribute("href", "#");
    testDocument.body.appendChild(link);

    const evt = new MouseEvent("click", { bubbles: true, cancelable: true });
    link.dispatchEvent(evt);

    expect(scrollSpy).not.toHaveBeenCalled();
    expect(evt.defaultPrevented).toBe(false);
  });

  it("does not intercept when target id is missing — lets in-document handlers run", () => {
    const link = testDocument.createElement("a");
    link.setAttribute("href", "#nonexistent");
    testDocument.body.appendChild(link);

    const evt = new MouseEvent("click", { bubbles: true, cancelable: true });
    link.dispatchEvent(evt);

    expect(scrollSpy).not.toHaveBeenCalled();
    expect(evt.defaultPrevented).toBe(false);
  });

  it("yields to a user handler that already called preventDefault", () => {
    const section = testDocument.createElement("section");
    section.id = "intro";
    testDocument.body.appendChild(section);

    const link = testDocument.createElement("a");
    link.setAttribute("href", "#intro");
    testDocument.body.appendChild(link);

    // A user-installed handler that suppresses default behavior. Capture
    // phase + preventDefault — our shim must see defaultPrevented and bail.
    testDocument.addEventListener(
      "click",
      (e) => {
        if (e.target === link) e.preventDefault();
      },
      true,
    );

    link.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));

    expect(scrollSpy).not.toHaveBeenCalled();
  });
});
