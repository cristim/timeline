import { useEffect, useState } from "react";
import { loadManifest, type Manifest } from "./manifest";

export function App() {
  const [manifest, setManifest] = useState<Manifest | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    loadManifest().then(setManifest, (e: unknown) => setError(String(e)));
  }, []);

  if (error) {
    return <p>Failed to load manifest: {error}</p>;
  }
  if (!manifest) {
    return <p>Loading manifest…</p>;
  }
  return (
    <main>
      <h1>Everything Timeline</h1>
      <p>
        dataset <code>{manifest.dataset}</code> · generated{" "}
        <code>{manifest.generated_at}</code> · {manifest.buckets.length} buckets
      </p>
    </main>
  );
}
