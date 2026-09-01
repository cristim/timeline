import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { CompareShell } from "./CompareShell";
import { ViewSwitcher } from "./ViewSwitcher";

describe("ViewSwitcher", () => {
  it("enables Map and Compare and labels the remaining views unavailable", () => {
    const html = renderToStaticMarkup(createElement(ViewSwitcher, {
      active: "map",
      onSwitch: () => undefined,
    }));
    expect(html).toContain('aria-pressed="true"');
    expect(html).toContain(">Map<");
    expect(html).toContain(">Compare<");
    expect(html.match(/disabled=""/g)).toHaveLength(3);
    expect(html.match(/Unavailable/g)).toHaveLength(3);
  });
});

describe("CompareShell", () => {
  it("renders honest empty, pending, and pair states", () => {
    expect(renderToStaticMarkup(createElement(CompareShell, { subjects: [] }))).toContain(
      "Choose a subject",
    );
    expect(renderToStaticMarkup(createElement(CompareShell, { subjects: ["earth"] }))).toContain(
      "Search for a second subject",
    );
    expect(renderToStaticMarkup(createElement(CompareShell, { subjects: ["earth", "mars"] })))
      .toContain("earth and mars");
  });
});
