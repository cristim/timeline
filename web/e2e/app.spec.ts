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
  // The map worker fetches basemap tiles; a dead worker leaves an empty
  // sphere with a clean console, so require at least one tile request.
  const tileLoaded = page.waitForRequest(/demotiles\.maplibre\.org\/tiles\/\d/, {
    timeout: 15_000,
  });
  await page.goto("./");
  await booted(page);
  await expect(page.locator(".bucket-badge")).toHaveText("T0");
  await expect(page.locator(".count")).toContainText("shown");
  await tileLoaded;
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

// A 1900-1995 view with the cursor pinned to 1942, inside the 1938 slice's
// window. Exponents are written without a "+" because a literal plus in a
// query string decodes as a space.
const CENTURY_VIEW = "?t0=-2.209e9&t1=7.889e8&tc=-8.558e8";
const V_T0 = -2.209e9;
const V_T1 = 7.889e8;

/** Viewport x of a time on the timeline canvas, plus the handle's y. */
async function cursorHandle(page: Page, t: number) {
  const box = (await page.locator(".timeline-canvas").boundingBox())!;
  return {
    x: box.x + ((t - V_T0) / (V_T1 - V_T0)) * box.width,
    y: box.y + 14, // the handle sits in the axis strip
  };
}

async function dragCursorTo(page: Page, from: number, to: number) {
  const a = await cursorHandle(page, from);
  const b = await cursorHandle(page, to);
  await page.mouse.move(a.x, a.y);
  await page.mouse.down();
  await page.mouse.move(b.x, b.y, { steps: 12 });
  await page.mouse.up();
}

test("the time cursor restores from a URL, drags, nudges, and unpins", async ({ page }) => {
  const w = watch(page);
  await page.goto(`./${CENTURY_VIEW}`);
  await booted(page);
  await expect(page.locator(".cur-label")).toContainText("1942");
  await expect(page.locator(".cursor-readout")).not.toHaveClass(/unpinned/);

  // Drag the handle from 1942 towards 1985.
  await dragCursorTo(page, -8.558e8, 4.7335e8);
  await expect(page.locator(".cur-label")).toContainText("198");
  // The URL write is debounced, and settles behind whatever layer the new
  // cursor position pulls in - so poll for it rather than assuming an SLA.
  await expect.poll(() => page.url()).toContain("tc=");

  // Arrow keys nudge it while the timeline has focus.
  const before = await page.locator(".cur-label").textContent();
  await page.locator(".timeline-canvas").focus();
  for (let i = 0; i < 5; i++) await page.keyboard.press("Shift+ArrowLeft");
  await expect(page.locator(".cur-label")).not.toHaveText(before!);

  // Unpinning returns it to following the centre of the view, and drops the
  // parameter from the URL.
  await page.locator(".cur-unpin").click();
  await expect(page.locator(".cursor-readout")).toHaveClass(/unpinned/);
  await expect.poll(() => page.url()).not.toContain("tc=");
  expect(w.errors, "console errors").toEqual([]);
  expect(w.notFound, "same-origin 404s").toEqual([]);
});

// DEV-6 M4's acceptance test: dragging the cursor over a covered era visibly
// changes the borders, with no tile server involved.
test("dragging the cursor swaps the historical border overlay", async ({ page }) => {
  const w = watch(page);
  await page.goto(`./${CENTURY_VIEW}`);
  await booted(page);
  await expect(page.locator(".era-chip")).toContainText("1938");
  await expect(page.locator(".era-chip")).not.toHaveClass(/empty/);
  // Let the basemap settle so the comparison below cannot be decided by a
  // tile that arrived late.
  await page.waitForTimeout(4000);
  const axis = await page.locator(".map-container").screenshot();

  // 1985 falls in the 1960 slice's window instead.
  await dragCursorTo(page, -8.558e8, 4.7335e8);
  await expect(page.locator(".era-chip")).toContainText("1960");
  await page.waitForTimeout(1500); // crossfade
  const soviet = await page.locator(".map-container").screenshot();
  expect(Buffer.compare(axis, soviet), "the map must visibly change").not.toBe(0);
  expect(w.errors).toEqual([]);
  expect(w.notFound).toEqual([]);
});

const SECONDS_PER_YEAR = 31_556_952;
/** The cursor time for a calendar year, matching web/src/lib/keyscheme.ts. */
function tcForYear(year: number) {
  return (year - 1970) * SECONDS_PER_YEAR;
}

/** Loads the app with the cursor pinned to `year`, view framed around it. */
async function gotoYear(page: Page, year: number, span: number) {
  const tc = tcForYear(year);
  await page.goto(`./?t0=${tc - span}&t1=${tc + span}&tc=${tc}`);
  await booted(page);
}

/**
 * Records which layer artifacts the client actually fetched. The dev-only
 * window hooks are stripped from the built bundle, so what the map is showing
 * is asserted through the two things that survive a production build: the URLs
 * requested, and the chip.
 */
function watchLayers(page: Page) {
  const fetched: string[] = [];
  page.on("response", (res) => {
    const m = /\/layers\/([a-z]+)\/(-?\d+)\.json$/.exec(new URL(res.url()).pathname);
    if (m && res.ok()) fetched.push(`${m[1]}/${m[2]}`);
  });
  return fetched;
}

// The whole point of replacing five hand-traced eras with a tiling dataset:
// there is no longer a date in recorded history that shows nothing.
test("every date in recorded history shows a map", async ({ page }) => {
  const w = watch(page);
  for (const year of [-5000, -500, 800, 1200, 1500, 1751, 1900, 1960, 2005]) {
    const layers = watchLayers(page);
    await gotoYear(page, year, 50 * SECONDS_PER_YEAR);
    const chip = page.locator(".era-chip");
    await expect(chip, `chip at ${year}`).not.toHaveClass(/empty/);
    await expect(chip, `chip at ${year}`).toContainText("world borders");
    await expect(chip, `chip at ${year}`).not.toHaveClass(/paleo/);
    // A political slice was actually downloaded, and deep time was not.
    await expect
      .poll(() => layers.filter((l) => l.startsWith("borders/")).length, {
        message: `borders fetched at ${year}`,
      })
      .toBeGreaterThan(0);
    expect(layers.filter((l) => l.startsWith("paleocoast/")), `paleo at ${year}`).toEqual([]);
  }
  expect(w.errors, "console errors").toEqual([]);
  expect(w.notFound, "same-origin 404s").toEqual([]);
});

// 1751 used to be the canonical "no curated era" date. It is covered now, so
// the empty state has to be tested somewhere it is still honest: past the end
// of the dataset entirely.
test("dates outside every layer say so instead of staying modern", async ({ page }) => {
  const layers = watchLayers(page);
  await gotoYear(page, 9000, 50 * SECONDS_PER_YEAR);
  await expect(page.locator(".era-chip")).toContainText("no map data");
  await expect(page.locator(".era-chip")).toHaveClass(/empty/);
  // The index answers "nothing covers this" without downloading a slice body.
  expect(layers).toEqual([]);
});

test("deep time renders reconstructed coastlines and hides the modern world", async ({ page }) => {
  const w = watch(page);
  const layers = watchLayers(page);
  await gotoYear(page, -250_000_000, 20_000_000 * SECONDS_PER_YEAR);

  const chip = page.locator(".era-chip");
  await expect(chip).toContainText("Ma");
  await expect(chip).toContainText("GPlates");
  await expect(chip).toHaveClass(/paleo/);

  await expect
    .poll(() => layers.filter((l) => l.startsWith("paleocoast/")).length)
    .toBeGreaterThan(0);
  // Political borders are meaningless here and must not be drawn.
  expect(layers.filter((l) => l.startsWith("borders/"))).toEqual([]);

  // The opaque ocean is what actually hides the modern basemap. Sample the
  // rendered canvas: with the globe filling the view, the centre pixel must be
  // paleo ocean or reconstructed land, never the basemap's pale pastel.
  await page.waitForTimeout(2500); // crossfade + first paint
  const centre = await mapCentrePixel(page);
  expect(centre.r, `centre pixel ${JSON.stringify(centre)}`).toBeLessThan(190);
  expect(centre.g, `centre pixel ${JSON.stringify(centre)}`).toBeLessThan(190);
  expect(w.errors, "console errors").toEqual([]);
  expect(w.notFound, "same-origin 404s").toEqual([]);
});

/**
 * The RGB at a fractional position of the map, read off a screenshot. The
 * dev-only window hooks are stripped from the built bundle, so what the globe
 * is actually painted with has to be read from pixels.
 */
async function mapPixel(page: Page, fx = 0.5, fy = 0.5) {
  const shot = await page.locator(".map-container").screenshot();
  const png = await page.evaluate(
    ([b64, sx, sy]) =>
      new Promise<[number, number, number]>((resolve) => {
        const img = new Image();
        img.onload = () => {
          const c = document.createElement("canvas");
          c.width = img.width;
          c.height = img.height;
          const ctx = c.getContext("2d")!;
          ctx.drawImage(img, 0, 0);
          const d = ctx.getImageData(
            Math.round(img.width * Number(sx)),
            Math.round(img.height * Number(sy)),
            1,
            1,
          ).data;
          resolve([d[0], d[1], d[2]]);
        };
        img.src = `data:image/png;base64,${b64}`;
      }),
    [shot.toString("base64"), String(fx), String(fy)],
  );
  return { r: png[0], g: png[1], b: png[2] };
}

const mapCentrePixel = (page: Page) => mapPixel(page);

// The one moment the map changes kind. A gap here blanks the world; an
// overlap would draw both at once.
test("deep time hands over to recorded history at the boundary", async ({ page }) => {
  // 123001 BC is the last year of the paleo layer's youngest slice.
  const before = watchLayers(page);
  await gotoYear(page, -123_001, 1000 * SECONDS_PER_YEAR);
  await expect(page.locator(".era-chip")).toHaveClass(/paleo/);
  await expect.poll(() => before.filter((l) => l.startsWith("paleocoast/")).length).toBeGreaterThan(0);
  expect(before.filter((l) => l.startsWith("borders/"))).toEqual([]);

  // 123000 BC is the first year of the political layer's oldest slice.
  const after = watchLayers(page);
  await gotoYear(page, -123_000, 1000 * SECONDS_PER_YEAR);
  const chip = page.locator(".era-chip");
  await expect(chip).not.toHaveClass(/paleo/);
  await expect(chip).not.toHaveClass(/empty/);
  await expect(chip).toContainText("123000 BC");
  await expect.poll(() => after.filter((l) => l.startsWith("borders/")).length).toBeGreaterThan(0);
});

// Scrubbing must walk the slices, not jump between a favoured few: each
// distinct slice the cursor passes through has to actually be shown.
test("scrubbing backwards visits each slice in turn", async ({ page }) => {
  const seen: string[] = [];
  for (const year of [2005, 1975, 1950, 1935, 1925, 1918, 1910, 1890]) {
    await gotoYear(page, year, 50 * SECONDS_PER_YEAR);
    const chip = page.locator(".era-chip");
    // The chip starts empty and fills once the slice settles and loads; read
    // it only after it has, or this races the fetch rather than testing it.
    await expect(chip, `chip at ${year}`).not.toHaveClass(/empty/);
    const label = await chip.textContent();
    seen.push(label!.replace(/^world borders · /, "").replace(/ · .*$/, ""));
  }
  // Strictly older at every step: none repeated, none skipped past.
  expect(seen).toEqual(["2000", "1960", "1945", "1930", "1920", "1914", "1900", "1880"]);
});

test("a war with curated fronts animates against the cursor", async ({ page }) => {
  const w = watch(page);
  await page.goto(`./${CENTURY_VIEW}&sel=eastern-front-wwii`);
  await booted(page);
  await expect(page.locator(".inspector h2")).toHaveText(/Eastern Front/);
  await expect(page.locator(".front-chip")).toContainText("1942 front line");
  await expect(page.locator(".front-chip")).toContainText("Stalingrad");

  // Two years later the nearest documented trace is a different one.
  await dragCursorTo(page, -8.558e8, -8.0e8);
  await expect(page.locator(".front-chip")).toContainText("1944 front line");
  await expect(page.locator(".front-chip")).not.toContainText("Stalingrad");

  // Past the end of the war the line is held, and says so rather than
  // pretending to know where a front was in 1985.
  await dragCursorTo(page, -8.0e8, 4.7335e8);
  await expect(page.locator(".front-chip")).toContainText("held");
  expect(w.errors).toEqual([]);
  expect(w.notFound).toEqual([]);
});

test("older than every reconstruction the globe says so and shows nothing", async ({ page }) => {
  const w = watch(page);
  const layers = watchLayers(page);
  await gotoYear(page, -2_000_000_000, 200_000_000 * SECONDS_PER_YEAR);

  const chip = page.locator(".era-chip");
  await expect(chip).toContainText("no reconstruction earlier than 540 Ma");
  await expect(chip).toHaveClass(/void/);
  expect(layers).toEqual([]);

  await page.waitForTimeout(2500);
  const centre = await mapCentrePixel(page);
  const px = JSON.stringify(centre);
  expect(centre.r, `not the pale basemap: ${px}`).toBeLessThan(90);
  expect(centre.g, `not the pale basemap: ${px}`).toBeLessThan(90);
  expect(centre.b, `not ocean blue: ${px}`).toBeLessThan(70);
  expect(w.errors, "console errors").toEqual([]);
});

test("before Earth exists the chip says that instead", async ({ page }) => {
  await gotoYear(page, -5_000_000_000, 200_000_000 * SECONDS_PER_YEAR);
  await expect(page.locator(".era-chip")).toContainText("Earth does not exist yet");
  await expect(page.locator(".era-chip")).toHaveClass(/void/);
});

test("recorded history replaces the modern basemap", async ({ page }) => {
  const w = watch(page);
  await gotoYear(page, 1500, 60 * SECONDS_PER_YEAR);
  await expect(page.locator(".era-chip")).toContainText("world borders · 1500");
  await page.waitForTimeout(3000); // slice + crossfade

  for (const [fx, fy] of [
    [0.5, 0.5], // Mediterranean: ocean
    [0.42, 0.55], // Africa: land base or a polity
    [0.36, 0.35], // Atlantic: ocean
    [0.45, 0.28], // Europe: polities
  ]) {
    const p = await mapPixel(page, fx, fy);
    expect(p.r, `pale basemap at ${fx},${fy}: ${JSON.stringify(p)}`).toBeLessThan(190);
    expect(p.g, `pale basemap at ${fx},${fy}: ${JSON.stringify(p)}`).toBeLessThan(190);
  }
  expect(w.errors, "console errors").toEqual([]);
});

test("hovering a polity names it", async ({ page }) => {
  await gotoYear(page, 1500, 60 * SECONDS_PER_YEAR);
  await expect(page.locator(".era-chip")).toContainText("world borders · 1500");
  await page.waitForTimeout(3000);

  const box = (await page.locator(".map-container").boundingBox())!;
  const cx = box.x + box.width / 2;
  const cy = box.y + box.height / 2;
  for (const [dx, dy] of [
    [60, 40],
    [0, 0],
    [-80, -40],
    [-160, 60],
    [120, -80],
  ]) {
    await page.mouse.move(cx + dx, cy + dy);
    await page.waitForTimeout(350);
    if (await page.locator(".map-tooltip").count()) break;
  }
  const tip = page.locator(".map-tooltip");
  await expect(tip).toBeVisible();
  await expect(tip).not.toBeEmpty();

  await page.mouse.move(cx + 300, cy - 260); // off the globe, onto empty sky
  await expect(tip).toHaveCount(0);
});

// A 1450-1460 view. The 10% band around the cursor is +/-1 year, so 1451
// excludes the fall of Constantinople (1453.4) and 1453.4 includes it. Both
// cursors sit inside the 1400 slice's window, so the map's only difference
// between them is the marker itself.
const NARROW = "t0=-1.6409615040e%2B10&t1=-1.6094045520e%2B10";
const TC_OFF = "-1.6378058088e%2B10"; // 1451
const TC_ON = "-1.6302321403e%2B10"; // 1453.4

test("a point event shows only near the cursor, and a selected one always", async ({ page }) => {
  const w = watch(page);

  await page.goto(`./?${NARROW}&tc=${TC_OFF}`);
  await booted(page);
  await expect(page.locator(".era-chip")).toContainText("world borders · 1400");
  await page.waitForTimeout(2500);
  await expect(page.locator(".count")).toHaveText("0 shown");
  const without = await page.locator(".map-container").screenshot();

  await page.goto(`./?${NARROW}&tc=${TC_ON}`);
  await booted(page);
  await expect(page.locator(".era-chip")).toContainText("world borders · 1400");
  await page.waitForTimeout(2500);
  await expect(page.locator(".count")).toHaveText("1 shown");
  const withIt = await page.locator(".map-container").screenshot();
  expect(Buffer.compare(without, withIt), "the marker must appear on the map").not.toBe(0);

  await page.goto(`./?${NARROW}&tc=${TC_OFF}&sel=fall-of-constantinople`);
  await booted(page);
  await expect(page.locator(".inspector h2")).toHaveText("Fall of Constantinople");
  await page.waitForTimeout(2500);
  await expect(page.locator(".count")).toHaveText("1 shown");
  expect(w.errors, "console errors").toEqual([]);
});

test("deep time is not littered with events from other eras", async ({ page }) => {
  await gotoYear(page, -250_000_000, 20_000_000 * SECONDS_PER_YEAR);
  await expect(page.locator(".era-chip")).toHaveClass(/paleo/);
  await page.waitForTimeout(2500);
  const n = Number((await page.locator(".count").textContent())!.split(" ")[0]);
  expect(n).toBeLessThanOrEqual(3);
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
