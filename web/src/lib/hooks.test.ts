import { afterEach, describe, expect, it, vi } from "vitest";
import { ArtifactHTTPError } from "./data";
import { entityFailure } from "./hooks";

afterEach(() => vi.restoreAllMocks());

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
