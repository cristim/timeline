/**
 * Publishes a value on `window` for the e2e suite to read. Dev server only:
 * `import.meta.env.DEV` is false in a build, so these are stripped from the
 * deployed bundle and the suite cannot depend on them when it runs against
 * the static artifact.
 */
export function devHook(name: string, value: unknown) {
  if (import.meta.env.DEV) {
    (window as unknown as Record<string, unknown>)[name] = value;
  }
}
