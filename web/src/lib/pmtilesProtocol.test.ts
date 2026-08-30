import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  add: vi.fn(),
  addProtocol: vi.fn(),
  fetchSources: [] as Array<{ url: string; chromeWindowsNoCache: boolean }>,
  get: vi.fn(),
  pmtilesConstructors: vi.fn(),
  protocolConstructors: vi.fn(),
  tile: vi.fn(),
}));

vi.mock("maplibre-gl", () => ({ addProtocol: mocks.addProtocol }));
vi.mock("pmtiles", () => ({
  FetchSource: class {
    readonly url: string;
    chromeWindowsNoCache = false;

    constructor(url: string) {
      this.url = url;
      mocks.fetchSources.push(this);
    }
  },
  PMTiles: class {
    constructor(source: unknown) {
      mocks.pmtilesConstructors(source);
    }
  },
  Protocol: class {
    readonly add = mocks.add;
    readonly get = mocks.get;
    readonly tile = mocks.tile;

    constructor() {
      mocks.protocolConstructors();
    }
  },
}));

describe("registerPMTilesProtocol", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    mocks.fetchSources.length = 0;
  });

  it("constructs and registers one protocol instance", async () => {
    const { registerPMTilesProtocol } = await import("./pmtilesProtocol");

    registerPMTilesProtocol();
    registerPMTilesProtocol();

    expect(mocks.protocolConstructors).toHaveBeenCalledTimes(1);
    expect(mocks.addProtocol).toHaveBeenCalledTimes(1);
    expect(mocks.addProtocol).toHaveBeenCalledWith("pmtiles", expect.any(Function));
  });

  it("opens each archive lazily with range caching disabled", async () => {
    const { registerPMTilesProtocol } = await import("./pmtilesProtocol");
    const controller = new AbortController();
    mocks.tile.mockResolvedValueOnce({ data: {} });

    registerPMTilesProtocol();
    const registered = mocks.addProtocol.mock.calls[0][1];
    await registered(
      { type: "json", url: "pmtiles://https://example.test/borders/1400.pmtiles" },
      controller,
    );

    expect(mocks.fetchSources).toEqual([
      { url: "https://example.test/borders/1400.pmtiles", chromeWindowsNoCache: true },
    ]);
    expect(mocks.pmtilesConstructors).toHaveBeenCalledWith(mocks.fetchSources[0]);
    expect(mocks.add).toHaveBeenCalledTimes(1);
    expect(mocks.tile).toHaveBeenCalledWith(
      { type: "json", url: "pmtiles://https://example.test/borders/1400.pmtiles" },
      controller,
    );
  });

  it("does not hide a live protocol failure", async () => {
    const { registerPMTilesProtocol } = await import("./pmtilesProtocol");
    const controller = new AbortController();
    const fetchError = new Error("Failed to fetch");
    mocks.tile.mockRejectedValueOnce(fetchError);

    registerPMTilesProtocol();
    const registered = mocks.addProtocol.mock.calls[0][1];

    await expect(
      registered(
        {
          type: "arrayBuffer",
          url: "pmtiles://https://example.test/borders/1400.pmtiles/1/0/0",
        },
        controller,
      ),
    ).rejects.toBe(fetchError);
  });
});
