import { addProtocol } from "maplibre-gl";
import { FetchSource, PMTiles, Protocol } from "pmtiles";

const SCHEME = "pmtiles://";

let protocol: Protocol | null = null;

export function registerPMTilesProtocol(): void {
  if (protocol) return;
  const nextProtocol = new Protocol();
  protocol = nextProtocol;
  addProtocol("pmtiles", (request, abortController) => {
    if (request.type === "json") {
      const archiveUrl = request.url.slice(SCHEME.length);
      if (!nextProtocol.get(archiveUrl)) {
        const source = new FetchSource(archiveUrl);
        // Chromium can reuse a cached response for a different byte range after navigation.
        source.chromeWindowsNoCache = true;
        nextProtocol.add(new PMTiles(source));
      }
    }
    return nextProtocol.tile(request, abortController);
  });
}
