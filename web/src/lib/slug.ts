const ENTITY_SLUG = /^[a-z0-9]+(?:-+[a-z0-9]+)*$/;

export function isValidEntitySlug(slug: string): boolean {
  return ENTITY_SLUG.test(slug);
}
