// The playwright-verify checklist for this app: boot cleanly, core flows,
// deep-link restore, back/forward, responsive spot check, no 404s on
// app-computed URLs. Runs against a prod-parity serving path (see config).
import { expect, test, type Page } from "@playwright/test";

/** Console errors + same-origin 404s collected per page; both must be empty. */
function watch(page: Page) {
  const errors: string[] = [];
  const notFound: string[] = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") errors.push(msg.text());
  });
  page.on("pageerror", (err) => errors.push(String(err)));
  page.on("response", (res) => {
    const sameOrigin = new URL(res.url()).origin === new URL(page.url()).origin;
    if (res.status() === 404 && sameOrigin) notFound.push(res.url());
  });
  return { errors, notFound };
}

async function booted(page: Page) {
  await expect(page.locator(".bucket-badge")).toHaveText(/T\d+/, { timeout: 15_000 });
  await expect(page.locator(".count")).not.toHaveText("…");
}

test("boots the whole-universe view with zero console errors", async ({ page }) => {
  const w = watch(page);
  await page.goto("./");
  await booted(page);
  await expect(page.locator(".bucket-badge")).toHaveText("T0");
  await expect(page.locator(".count")).toContainText("shown");
  expect(w.errors, "console errors").toEqual([]);
  expect(w.notFound, "same-origin 404s").toEqual([]);
});

test("search selects an entity, shows claims, and focuses the timeline", async ({ page }) => {
  const w = watch(page);
  await page.goto("./");
  await booted(page);
  await page.locator(".searchbox input").fill("tyranno");
  await page.locator(".search-results li", { hasText: "Tyrannosaurus rex" }).click();
  await expect(page.locator(".inspector h2")).toHaveText("Tyrannosaurus rex");
  // Claims model visible: mass synthesized from multiple sourced claims.
  const mass = page.locator(".prop", { hasText: "mass" }).first();
  await expect(mass.locator(".claims-n")).toContainText("claims");
  // Timeline jumped to deep time.
  await expect(page.locator(".bucket-badge")).toHaveText(/T[2-6]/);
  expect(w.errors).toEqual([]);
  expect(w.notFound).toEqual([]);
});

test("a shared URL cold-loads and restores the exact view", async ({ page }) => {
  const w = watch(page);
  await page.goto("./?t0=-8.9e8&t1=-8.1e8&sel=battle-of-stalingrad&cats=war");
  await booted(page);
  await expect(page.locator(".inspector h2")).toHaveText("Battle of Stalingrad");
  await expect(page.locator(".bucket-badge")).toHaveText("T10");
  await expect(page.locator(".cat-chip.on")).toHaveCount(1);
  expect(w.errors).toEqual([]);
  expect(w.notFound).toEqual([]);
});

test("back/forward walks selection history", async ({ page }) => {
  await page.goto("./?t0=-8.9e8&t1=-8.1e8&sel=battle-of-stalingrad");
  await booted(page);
  await expect(page.locator(".inspector h2")).toHaveText("Battle of Stalingrad");
  await page.locator(".chip", { hasText: "Eastern Front" }).first().click();
  await expect(page.locator(".inspector h2")).toHaveText(/Eastern Front/);
  // pushState is debounced; give it a beat before navigating history
  await page.waitForTimeout(500);
  await page.goBack();
  await expect(page.locator(".inspector h2")).toHaveText("Battle of Stalingrad");
  await page.goForward();
  await expect(page.locator(".inspector h2")).toHaveText(/Eastern Front/);
});

test("timeline lanes show items starting/ending in view, capped at 100", async ({ page }) => {
  await page.goto("./?t0=-1.241524800e%2B9&t1=-4.836240000e%2B8");
  await booted(page);
  const countText = await page.locator(".count").textContent();
  const n = Number(countText?.split(" ")[0]);
  expect(n).toBeGreaterThan(0);
  expect(n).toBeLessThanOrEqual(100);
});

test("timeline resizes via the drag handle and collapses below the minimum", async ({ page }) => {
  await page.goto("./");
  await booted(page);
  const shell = page.locator(".timeline-shell");
  const startH = (await shell.boundingBox())!.height;
  const handle = page.locator(".tl-resize-handle");
  const hb = (await handle.boundingBox())!;

  // Drag up: grow.
  await page.mouse.move(hb.x + 300, hb.y + 4);
  await page.mouse.down();
  await page.mouse.move(hb.x + 300, hb.y - 120, { steps: 5 });
  await page.mouse.up();
  const grownH = (await shell.boundingBox())!.height;
  expect(grownH).toBeGreaterThan(startH + 80);

  // Drag far down: collapses to the status bar; canvas unmounts.
  const hb2 = (await handle.boundingBox())!;
  await page.mouse.move(hb2.x + 300, hb2.y + 4);
  await page.mouse.down();
  await page.mouse.move(hb2.x + 300, hb2.y + grownH, { steps: 5 });
  await page.mouse.up();
  await expect(shell).toHaveClass(/collapsed/);
  await expect(page.locator(".timeline-canvas")).toHaveCount(0);

  // Expand button restores it.
  await page.locator(".tl-toggle").click();
  await expect(page.locator(".timeline-canvas")).toBeVisible();
  expect((await shell.boundingBox())!.height).toBeGreaterThanOrEqual(140);
});

test("phone viewport still renders the three areas", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const w = watch(page);
  await page.goto("./");
  await booted(page);
  await expect(page.locator(".timeline-canvas")).toBeVisible();
  await expect(page.locator(".map-container")).toBeVisible();
  await expect(page.locator(".searchbox input")).toBeVisible();
  expect(w.errors).toEqual([]);
});
