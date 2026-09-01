// @vitest-environment jsdom

import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ArtifactHTTPError, type EntityDoc } from "./data";
import { entityFailure, entityRequestKey, entityResultIsCurrent, useEntityState, type EntityState } from "./hooks";
import type { Manifest } from "./manifest";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("entity load failures", () => {
  it("treats a valid-slug 404 as a quiet user-visible missing state", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    expect(entityFailure(new ArtifactHTTPError("entity/missing.json", 404))).toEqual({
      missing: true,
      error: null,
    });
    expect(consoleError).not.toHaveBeenCalled();
  });

  it("logs non-404 artifact failures and retains their message", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    expect(entityFailure(new SyntaxError("invalid JSON"))).toEqual({
      missing: false,
      error: "SyntaxError: invalid JSON",
    });
    expect(consoleError).toHaveBeenCalledOnce();
  });
});

describe("entity dataset rollover", () => {
  it("gives same-slug requests from different datasets distinct identities", () => {
    const oldRequest = entityRequestKey("dataset-a", "earth");
    const newRequest = entityRequestKey("dataset-b", "earth");
    expect(newRequest).not.toBe(oldRequest);
    expect(entityResultIsCurrent(newRequest, oldRequest)).toBe(false);
    expect(entityResultIsCurrent(newRequest, newRequest)).toBe(true);
  });

  it("clears the old document and ignores its late fetch after a same-slug rollover", async () => {
    (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
      .IS_REACT_ACT_ENVIRONMENT = true;
    const pending = new Map<string, ReturnType<typeof deferred<Response>>>();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const request = deferred<Response>();
      pending.set(String(input), request);
      return request.promise;
    }));

    let latest: EntityState | null = null;
    function Probe({ manifest }: { manifest: Manifest }) {
      latest = useEntityState(manifest, "earth");
      return createElement("output", null, latest.doc?.name ?? "");
    }

    const container = document.createElement("div");
    const root = createRoot(container);
    const first = { dataset: "rollover-a" } as Manifest;
    const second = { dataset: "rollover-b" } as Manifest;
    await act(async () => root.render(createElement(Probe, { manifest: first })));
    const oldRequest = pending.get("/v/rollover-a/entity/earth.json");
    expect(oldRequest).toBeDefined();

    await act(async () => root.render(createElement(Probe, { manifest: second })));
    expect(latest).toMatchObject({ slug: "earth", doc: null, loading: true });
    const newRequest = pending.get("/v/rollover-b/entity/earth.json");
    expect(newRequest).toBeDefined();

    await act(async () => {
      oldRequest!.resolve(jsonResponse(entityDoc("earth", "old dataset")));
      await oldRequest!.promise;
      await Promise.resolve();
    });
    expect(latest).toMatchObject({ slug: "earth", doc: null, loading: true });

    await act(async () => {
      newRequest!.resolve(jsonResponse(entityDoc("earth", "new dataset")));
      await newRequest!.promise;
      await Promise.resolve();
    });
    expect(latest).toMatchObject({ slug: "earth", loading: false });
    expect((latest as EntityState | null)?.doc?.name).toBe("new dataset");
    await act(async () => root.unmount());
  });

  it("clears an already-rendered document immediately when its dataset changes", async () => {
    (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
      .IS_REACT_ACT_ENVIRONMENT = true;
    const pending = new Map<string, ReturnType<typeof deferred<Response>>>();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const request = deferred<Response>();
      pending.set(String(input), request);
      return request.promise;
    }));

    let latest: EntityState | null = null;
    const renders: { dataset: string; name: string | null }[] = [];
    function Probe({ manifest }: { manifest: Manifest }) {
      latest = useEntityState(manifest, "earth");
      renders.push({ dataset: manifest.dataset, name: latest.doc?.name ?? null });
      return createElement("output", null, latest.doc?.name ?? "");
    }

    const root = createRoot(document.createElement("div"));
    await act(async () => root.render(createElement(Probe, {
      manifest: { dataset: "loaded-rollover-a" } as Manifest,
    })));
    const oldRequest = pending.get("/v/loaded-rollover-a/entity/earth.json");
    expect(oldRequest).toBeDefined();
    await act(async () => {
      oldRequest!.resolve(jsonResponse(entityDoc("earth", "loaded old dataset")));
      await oldRequest!.promise;
      await Promise.resolve();
    });
    expect((latest as EntityState | null)?.doc?.name).toBe("loaded old dataset");

    await act(async () => root.render(createElement(Probe, {
      manifest: { dataset: "loaded-rollover-b" } as Manifest,
    })));
    expect(renders.filter((render) => render.dataset === "loaded-rollover-b"))
      .not.toContainEqual({ dataset: "loaded-rollover-b", name: "loaded old dataset" });
    expect(latest).toMatchObject({ slug: "earth", doc: null, loading: true });
    expect(pending.get("/v/loaded-rollover-b/entity/earth.json")).toBeDefined();
    await act(async () => root.unmount());
  });

  it("surfaces a mismatched payload slug before App can pin it", async () => {
    (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
      .IS_REACT_ACT_ENVIRONMENT = true;
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(entityDoc("mars", "wrong entity"))));
    let latest: EntityState | null = null;
    const manifest = { dataset: "slug-mismatch" } as Manifest;
    function Probe() {
      latest = useEntityState(manifest, "earth");
      return createElement("output");
    }
    const root = createRoot(document.createElement("div"));
    await act(async () => {
      root.render(createElement(Probe));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(latest).toMatchObject({ slug: "earth", doc: null, loading: false });
    expect((latest as EntityState | null)?.error).toContain("slug does not match");
    expect(consoleError).toHaveBeenCalledOnce();
    await act(async () => root.unmount());
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function jsonResponse(doc: EntityDoc): Response {
  return { ok: true, json: async () => doc } as Response;
}

function entityDoc(slug: string, name: string): EntityDoc {
  return {
    slug,
    type: "place",
    name,
    temporal: { t0: 0, t1: 1, precision: "year", status: "documented" },
    categories: ["earth"],
    importance: 1,
    links: {},
  };
}
