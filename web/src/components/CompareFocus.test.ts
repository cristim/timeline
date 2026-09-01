// @vitest-environment jsdom

import { act, createElement, useState } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it } from "vitest";
import type { EntityState } from "../lib/hooks";
import { CompareContent } from "./CompareShell";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
  .IS_REACT_ACT_ENVIRONMENT = true;

const emptyStates: [EntityState, EntityState] = [
  { slug: null, doc: null, loading: false, missing: false, error: null },
  { slug: null, doc: null, loading: false, missing: false, error: null },
];

afterEach(() => {
  document.body.replaceChildren();
});

it("moves focus into Compare and returns it to the entry control on close", async () => {
  function Harness() {
    const [open, setOpen] = useState(false);
    return createElement("div", null,
      createElement("button", { onClick: () => setOpen(true) }, "Compare"),
      open && createElement(CompareContent, {
        subjects: [], propertyDefs: {}, states: emptyStates, onClose: () => setOpen(false),
      }),
    );
  }

  const root = createRoot(document.body.appendChild(document.createElement("div")));
  await act(async () => root.render(createElement(Harness)));
  const trigger = document.querySelector("button") as HTMLButtonElement;
  trigger.focus();
  await act(async () => trigger.click());
  expect(document.activeElement).toBe(document.querySelector(".compare-shell"));

  const close = document.querySelector('[aria-label="Close Compare"]') as HTMLButtonElement;
  await act(async () => close.click());
  expect(document.activeElement).toBe(trigger);
  await act(async () => root.unmount());
});

it("closes on Escape and focuses Map when a deep link has no entry control", async () => {
  function Harness() {
    const [open, setOpen] = useState(true);
    return createElement("div", null,
      createElement("button", { "data-view-mode": "map" }, "Map"),
      open && createElement(CompareContent, {
        subjects: [], propertyDefs: {}, states: emptyStates, onClose: () => setOpen(false),
      }),
    );
  }

  const root = createRoot(document.body.appendChild(document.createElement("div")));
  await act(async () => root.render(createElement(Harness)));
  const shell = document.querySelector(".compare-shell") as HTMLElement;
  expect(document.activeElement).toBe(shell);
  await act(async () => shell.dispatchEvent(new KeyboardEvent("keydown", {
    key: "Escape", bubbles: true, cancelable: true,
  })));
  expect(document.querySelector(".compare-shell")).toBeNull();
  expect(document.activeElement).toBe(document.querySelector('[data-view-mode="map"]'));
  await act(async () => root.unmount());
});

it("does not override focus when an outside view control closes Compare", async () => {
  function Harness() {
    const [open, setOpen] = useState(true);
    return createElement("div", null,
      createElement("button", { "data-view-mode": "map", onClick: () => setOpen(false) }, "Map"),
      open && createElement(CompareContent, {
        subjects: [], propertyDefs: {}, states: emptyStates, onClose: () => setOpen(false),
      }),
    );
  }

  const root = createRoot(document.body.appendChild(document.createElement("div")));
  await act(async () => root.render(createElement(Harness)));
  const map = document.querySelector('[data-view-mode="map"]') as HTMLButtonElement;
  map.focus();
  await act(async () => map.click());
  expect(document.activeElement).toBe(map);
  await act(async () => root.unmount());
});
