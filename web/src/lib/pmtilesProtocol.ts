import { addProtocol } from "maplibre-gl";
import { FetchSource, PMTiles, Protocol } from "pmtiles";

const SCHEME = "pmtiles://";

let protocol: Protocol | null = null;

export function registerPMTilesProtocol(): void {
  if (protocol) return;
  const nextProtocol = new Protocol();
  protocol = nextProtocol;
  addProtocol("pmtiles", async (request, abortController) => {
    if (request.type === "json") {
      const archiveUrl = request.url.slice(SCHEME.length);
      if (!nextProtocol.get(archiveUrl)) {
        const source = new FetchSource(archiveUrl);
        // Chromium (all platforms, not just the Windows case pmtiles detects
        // itself) can answer a range request from a cached 206 for a
        // DIFFERENT range after navigation; the read then fails with a bare
        // "Failed to fetch" and the layer gets holes. Reproduced in the e2e
        // suite on macOS. Correct reads beat cached reads for small ranges.
        source.chromeWindowsNoCache = true;
        nextProtocol.add(new PMTiles(source));
      }
    }
    try {
      return await nextProtocol.tile(request, abortController);
    } catch (e) {
      // Chromium rejects a range read cancelled mid-flight with a generic
      // TypeError ("Failed to fetch"), not an AbortError. Normalize it so a
      // cancellation never reads as a load failure downstream. (pmtiles also
      // coalesces reads across callers, so one cancellation can reject other
      // live waiters the same way; SlotPair's retry absorbs those.)
      if (abortController.signal.aborted && (e as { name?: string })?.name !== "AbortError") {
        const abort = new Error("PMTiles fetch aborted");
        abort.name = "AbortError";
        throw abort;
      }
      throw e;
    }
  });
}
