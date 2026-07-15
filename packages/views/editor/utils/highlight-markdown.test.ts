import { describe, it, expect } from "vitest";
import { highlightToHtml } from "./highlight-markdown";

describe("highlightToHtml", () => {
  it.each([
    ["a ==hi== b", "a <mark>hi</mark> b"],
    ["==**bold**==", "<mark>**bold**</mark>"],
    ["==a== and ==b==", "<mark>a</mark> and <mark>b</mark>"],
    ["== spaced ==", "== spaced =="],
    ["====", "===="],
    ["plain **bold** _italic_ text", "plain **bold** _italic_ text"],
    ["`a ==b== c`", "`a ==b== c`"],
    ["```\nx ==y== z\n```", "```\nx ==y== z\n```"],
    ["$a ==b== c$", "$a ==b== c$"],
    ["==hi== `x ==y==`", "<mark>hi</mark> `x ==y==`"],
    ["if a == b then", "if a == b then"],
    ["==a `b==c` d==", "<mark>a `b==c` d</mark>"],
    ["==a $b==c$ d==", "<mark>a $b==c$ d</mark>"],
    ["==a\n\nb==", "==a\n\nb=="],
    ["==a\nb==", "<mark>a\nb</mark>"],
    ["==a\r\n\r\nb==", "==a\r\n\r\nb=="],
    ["==a\r\nb==", "<mark>a\r\nb</mark>"],
  ])("transforms %j", (input, expected) => {
    expect(highlightToHtml(input)).toBe(expected);
  });
});
