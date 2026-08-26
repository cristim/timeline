// Cosmic zoom: when the user zooms out past the whole globe, the map hands
// off to this canvas, which continues the same wheel gesture out through the
// Moon's orbit, the solar system, the Milky Way, and the observable universe.
// Zooming back in hands control back to the globe. Procedural and stylized to
// the app palette - this is orientation scenery, not an astronomy engine.
//
// Continuity rules that make the levels feel nested rather than swapped:
// - one eased display zoom (spring toward the target) drives every frame;
// - each scene shrinks TOWARD its anchor inside the next scene (Earth into
//   the solar system's Earth orbit, the Sun into the galaxy's "you are here"
//   marker, the galaxy into one dot of the cosmic web);
// - below the handback threshold the whole canvas fades out, revealing the
//   real MapLibre globe underneath - no cut on exit.
import { useCallback, useEffect, useRef } from "react";
import { captureGlobeSprite } from "./MapView";

interface Props {
  /** Space zoom s: 0 = handing back to the map, 4 = observable universe. */
  s: number;
  onZoom: (next: number) => void; // clamped by the caller; <=0 exits to map
  onSelect: (slug: string) => void;
}

export const SPACE_MAX = 4;

/** Below this display zoom the canvas is fully transparent (globe visible). */
const HANDBACK_FADE = 0.3;

// Scale captions per zoom band (the "you are here" ruler).
const scaleCaptions: [number, string][] = [
  [0.0, "Earth & Moon · ~1 million km"],
  [1.0, "Solar System · ~80 AU"],
  [2.0, "Milky Way · ~100,000 light-years"],
  [3.0, "Local Group & cosmic web · ~10 million ly"],
  [3.6, "Observable universe · ~93 billion ly"],
];

export function SpaceView({ s, onZoom, onSelect }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const sRef = useRef(s);
  sRef.current = s;
  const dispRef = useRef(0); // eased display zoom; starts at 0 = fade-in from globe
  const onZoomRef = useRef(onZoom);
  onZoomRef.current = onZoom;
  const hitsRef = useRef<{ x: number; y: number; r: number; slug: string }[]>([]);
  // The real globe, captured at handoff, becomes the Earth texture - the
  // sphere in space is the same pixels the user was just looking at. Null
  // until the map has rendered (cold loads with ?space= retry below).
  const earthSpriteRef = useRef<HTMLCanvasElement | null>(null);
  const spriteRetryRef = useRef(0);

  const draw = useCallback(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const dpr = window.devicePixelRatio || 1;
    const w = canvas.clientWidth;
    const h = canvas.clientHeight;
    if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
      canvas.width = w * dpr;
      canvas.height = h * dpr;
    }
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const z = dispRef.current;
    const cx = w / 2;
    const cy = h / 2;
    hitsRef.current = [];

    // Handback: fade the whole scene out over the real globe.
    canvas.style.opacity = `${clamp01(z / HANDBACK_FADE)}`;

    ctx.fillStyle = "#04060c";
    ctx.fillRect(0, 0, w, h);
    drawStars(ctx, w, h, Math.min(1, z + 0.3));

    // Scene alphas: eased crossfades between neighbouring scales.
    const earthA = ramp(z, -1, 0, 0.8, 1.6);
    const solarA = ramp(z, 0.7, 1.3, 1.8, 2.6);
    const galaxyA = ramp(z, 1.7, 2.4, 2.8, 3.5);
    const cosmosA = ramp(z, 2.7, 3.4, 99, 99);

    // Nested anchors (each scene shrinks toward its home in the next one).
    const solar = solarLayout(cx, cy, h, z);
    const galaxy = galaxyLayout(cx, cy, h, w, z);
    const cosmosAnchor = { x: w * 0.62, y: h * 0.44 };

    // Galaxy drifts into its cosmic-web dot.
    const gT = smoothstep(2.7, 3.4, z);
    const gCx = lerp(galaxy.cx, cosmosAnchor.x, gT);
    const gCy = lerp(galaxy.cy, cosmosAnchor.y, gT);

    // Solar system drifts into the galaxy's "you are here" marker (which
    // itself may be drifting toward the cosmic web).
    const sunMark = { x: gCx + galaxy.R * 0.42, y: gCy + galaxy.R * 0.12 };
    const sT = smoothstep(1.7, 2.4, z);
    const sCx = lerp(solar.cx, sunMark.x, sT);
    const sCy = lerp(solar.cy, sunMark.y, sT);

    // Earth scene drifts into the solar system's Earth orbit position.
    const earthPos = planetPosition(2, sCx, sCy, h, z);
    const eT = smoothstep(0.7, 1.3, z);
    const eCx = lerp(cx, earthPos.x, eT);
    const eCy = lerp(cy, earthPos.y, eT);

    if (cosmosA > 0) drawCosmos(ctx, w, h, z, cosmosA, cosmosAnchor);
    if (galaxyA > 0) drawGalaxy(ctx, gCx, gCy, galaxy.R, galaxyA);
    if (solarA > 0) drawSolarSystem(ctx, sCx, sCy, h, z, solarA, hitsRef.current);
    if (earthA > 0)
      drawEarthMoon(ctx, eCx, eCy, h, z, earthA, hitsRef.current, earthSpriteRef.current);

    // Scale caption + hint
    ctx.font = "12px ui-monospace, monospace";
    ctx.fillStyle = "#6fd4d0";
    let caption = scaleCaptions[0][1];
    for (const [minZ, text] of scaleCaptions) {
      if (z >= minZ) caption = text;
    }
    ctx.fillText(caption, 16, h - 18);
    ctx.fillStyle = "#5d6880";
    ctx.font = "11px system-ui, sans-serif";
    ctx.fillText("scroll up to zoom back to Earth", 16, h - 36);
  }, []);

  // Continuous ease loop: the display zoom springs toward the target, so both
  // wheel steps and the mount/unmount boundaries feel animated.
  useEffect(() => {
    earthSpriteRef.current = captureGlobeSprite();
    let raf = 0;
    const tick = () => {
      // Re-capture the globe periodically: cold loads land here before the
      // map has rendered (or with only the pale untiled sphere), and the
      // sprite upgrades as soon as basemap tiles finish loading underneath.
      if (spriteRetryRef.current++ % 30 === 0) {
        earthSpriteRef.current = captureGlobeSprite() ?? earthSpriteRef.current;
      }
      const target = sRef.current;
      const d = dispRef.current;
      const next = Math.abs(target - d) < 0.001 ? target : d + (target - d) * 0.16;
      if (next !== d) {
        dispRef.current = next;
      }
      // Once the target sits at the exit floor and the fade has completed,
      // finish the handback ourselves - an invisible overlay must never stay
      // mounted over the map.
      if (target <= 0.02 && next < 0.1) {
        onZoomRef.current(0);
      }
      draw();
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [draw]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      // Same wheel convention as the map and timeline: scroll down = zoom
      // out (deeper into space), scroll up = zoom back toward Earth.
      // Optimistic ref update so several events per frame accumulate.
      let next = sRef.current + e.deltaY * 0.0016;
      if (next <= 0 && dispRef.current > 0.12) {
        // Let the eased display catch up (and the canvas fade out over the
        // globe) before actually unmounting - no hard cut on handback.
        next = 0.01;
      }
      sRef.current = next;
      onZoomRef.current(next);
    };
    canvas.addEventListener("wheel", onWheel, { passive: false });
    return () => canvas.removeEventListener("wheel", onWheel);
  }, []);

  const onClick = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - rect.left;
    const py = e.clientY - rect.top;
    const hit = hitsRef.current.find((c) => Math.hypot(c.x - px, c.y - py) <= c.r + 6);
    if (hit) onSelect(hit.slug);
  };

  return <canvas ref={canvasRef} className="space-canvas" onClick={onClick} />;
}

/** 0 before a, eases to 1 by b, holds, eases out between c and d. */
function ramp(z: number, a: number, b: number, c: number, d: number): number {
  if (z <= a || z >= d) return 0;
  if (z < b) return smoothstep(a, b, z);
  if (z > c) return 1 - smoothstep(c, d, z);
  return 1;
}

function smoothstep(a: number, b: number, z: number): number {
  const t = clamp01((z - a) / (b - a));
  return t * t * (3 - 2 * t);
}

function clamp01(v: number): number {
  return Math.max(0, Math.min(1, v));
}

function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

function mulberry32(seed: number): () => number {
  let a = seed;
  return () => {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// Planet angles are fixed (same seeded sequence every frame) so positions are
// stable and other scenes can anchor to them.
const PLANET_ANGLES: number[] = (() => {
  const rand = mulberry32(11);
  return Array.from({ length: 8 }, () => rand() * Math.PI * 2);
})();

function solarScale(z: number): number {
  return Math.pow(0.5, z - 1.3);
}

function solarLayout(cx: number, cy: number, _h: number, _z: number) {
  return { cx, cy };
}

function planetOrbit(i: number, h: number, z: number): number {
  return (h * 0.055 * (i + 1.6) + h * 0.01 * Math.pow(1.55, i)) * solarScale(z);
}

function planetPosition(i: number, cx: number, cy: number, h: number, z: number) {
  const orbit = planetOrbit(i, h, z);
  return { x: cx + Math.cos(PLANET_ANGLES[i]) * orbit, y: cy + Math.sin(PLANET_ANGLES[i]) * orbit };
}

function galaxyLayout(cx: number, cy: number, h: number, _w: number, z: number) {
  return { cx, cy, R: h * 0.38 * Math.pow(0.55, Math.max(0, z - 2.4)) };
}

function drawStars(ctx: CanvasRenderingContext2D, w: number, h: number, alpha: number) {
  const rand = mulberry32(42);
  ctx.globalAlpha = alpha;
  for (let i = 0; i < 320; i++) {
    const x = rand() * w;
    const y = rand() * h;
    const r = rand() * 1.1 + 0.2;
    ctx.fillStyle = rand() > 0.85 ? "#a9c3e8" : "#e8e4d8";
    ctx.globalAlpha = alpha * (0.25 + rand() * 0.6);
    ctx.beginPath();
    ctx.arc(x, y, r, 0, Math.PI * 2);
    ctx.fill();
  }
  ctx.globalAlpha = 1;
}

function drawEarthMoon(
  ctx: CanvasRenderingContext2D,
  cx: number,
  cy: number,
  h: number,
  z: number,
  alpha: number,
  hits: { x: number; y: number; r: number; slug: string }[],
  sprite: HTMLCanvasElement | null,
) {
  // Earth shrinks from filling much of the view down to the size of its dot
  // in the solar-system scene, which it drifts into (anchor continuity).
  const r = Math.max(2.2, h * 0.34 * Math.pow(0.14, z));
  ctx.globalAlpha = alpha;
  if (sprite && r > 3) {
    // The captured globe: the same pixels the user was just looking at.
    ctx.save();
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    ctx.clip();
    ctx.drawImage(sprite, cx - r, cy - r, r * 2, r * 2);
    // Soft terminator shading so it reads as a lit sphere.
    const shade = ctx.createRadialGradient(cx - r * 0.4, cy - r * 0.4, r * 0.2, cx, cy, r * 1.05);
    shade.addColorStop(0, "rgba(255,255,255,0.06)");
    shade.addColorStop(0.75, "rgba(4,6,12,0)");
    shade.addColorStop(1, "rgba(4,6,12,0.55)");
    ctx.fillStyle = shade;
    ctx.fillRect(cx - r, cy - r, r * 2, r * 2);
    ctx.restore();
  } else {
    const grad = ctx.createRadialGradient(cx - r * 0.35, cy - r * 0.35, r * 0.1, cx, cy, r);
    grad.addColorStop(0, "#7db4d8");
    grad.addColorStop(0.55, "#2e6ea3");
    grad.addColorStop(1, "#12283f");
    ctx.fillStyle = grad;
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    ctx.fill();
  }
  hits.push({ x: cx, y: cy, r: Math.max(r, 10), slug: "earth" });

  // Moon orbit grows relative to the shrinking Earth, then collapses with it.
  const orbit = r * 6 + h * 0.03 * Math.max(0, 1 - z) * 4;
  ctx.strokeStyle = "rgba(154, 163, 181, 0.35)";
  ctx.setLineDash([3, 5]);
  ctx.beginPath();
  ctx.arc(cx, cy, orbit, 0, Math.PI * 2);
  ctx.stroke();
  ctx.setLineDash([]);
  const ma = -0.7;
  ctx.fillStyle = "#c9c4b8";
  ctx.beginPath();
  ctx.arc(cx + Math.cos(ma) * orbit, cy + Math.sin(ma) * orbit, Math.max(1.2, r * 0.27), 0, Math.PI * 2);
  ctx.fill();
  if (alpha > 0.5 && r > 6) {
    ctx.fillStyle = "#9aa3b5";
    ctx.font = "11px system-ui, sans-serif";
    ctx.fillText("Moon", cx + Math.cos(ma) * orbit + 8, cy + Math.sin(ma) * orbit + 4);
  }
  ctx.globalAlpha = 1;
}

const planets: { name: string; color: string; size: number; slug?: string }[] = [
  { name: "Mercury", color: "#b5a08a", size: 2 },
  { name: "Venus", color: "#d9c08a", size: 3 },
  { name: "Earth", color: "#5b9bd5", size: 3.2, slug: "earth" },
  { name: "Mars", color: "#c96b4a", size: 2.5 },
  { name: "Jupiter", color: "#d9a45a", size: 6 },
  { name: "Saturn", color: "#c9b458", size: 5 },
  { name: "Uranus", color: "#8ac3c9", size: 4 },
  { name: "Neptune", color: "#7d94c9", size: 4 },
];

function drawSolarSystem(
  ctx: CanvasRenderingContext2D,
  cx: number,
  cy: number,
  h: number,
  z: number,
  alpha: number,
  hits: { x: number; y: number; r: number; slug: string }[],
) {
  const scale = solarScale(z);
  ctx.globalAlpha = alpha;
  // Sun
  const sunR = Math.max(2.5, 9 * scale + 3 * Math.min(1, scale * 2));
  const glow = ctx.createRadialGradient(cx, cy, 0, cx, cy, sunR * 4);
  glow.addColorStop(0, "rgba(232, 180, 90, 0.9)");
  glow.addColorStop(0.3, "rgba(232, 180, 90, 0.25)");
  glow.addColorStop(1, "rgba(232, 180, 90, 0)");
  ctx.fillStyle = glow;
  ctx.beginPath();
  ctx.arc(cx, cy, sunR * 4, 0, Math.PI * 2);
  ctx.fill();
  ctx.fillStyle = "#f0d9a8";
  ctx.beginPath();
  ctx.arc(cx, cy, sunR, 0, Math.PI * 2);
  ctx.fill();
  hits.push({ x: cx, y: cy, r: sunR + 4, slug: "sun" });

  // Log-spaced orbits keep Neptune on screen without hiding Mercury.
  planets.forEach((p, i) => {
    const orbit = planetOrbit(i, h, z);
    ctx.strokeStyle = p.slug ? "rgba(232, 180, 90, 0.4)" : "rgba(154, 163, 181, 0.22)";
    ctx.beginPath();
    ctx.arc(cx, cy, orbit, 0, Math.PI * 2);
    ctx.stroke();
    const pos = planetPosition(i, cx, cy, h, z);
    ctx.fillStyle = p.color;
    ctx.beginPath();
    ctx.arc(pos.x, pos.y, Math.max(1.2, p.size * scale), 0, Math.PI * 2);
    ctx.fill();
    if (p.slug) hits.push({ x: pos.x, y: pos.y, r: 8, slug: p.slug });
    if (alpha > 0.6 && scale > 0.45) {
      ctx.fillStyle = p.slug ? "#e8b45a" : "#5d6880";
      ctx.font = "10px system-ui, sans-serif";
      ctx.fillText(p.name, pos.x + 6, pos.y - 5);
    }
  });
  if (alpha > 0.6 && scale > 0.3) {
    ctx.fillStyle = "#e8b45a";
    ctx.font = "11px system-ui, sans-serif";
    ctx.fillText("Sun", cx + sunR + 6, cy + 4);
  }
  ctx.globalAlpha = 1;
}

function drawGalaxy(
  ctx: CanvasRenderingContext2D,
  cx: number,
  cy: number,
  R: number,
  alpha: number,
) {
  ctx.globalAlpha = alpha;
  // Core
  const core = ctx.createRadialGradient(cx, cy, 0, cx, cy, Math.max(1, R * 0.35));
  core.addColorStop(0, "rgba(232, 228, 216, 0.9)");
  core.addColorStop(1, "rgba(232, 228, 216, 0)");
  ctx.fillStyle = core;
  ctx.beginPath();
  ctx.arc(cx, cy, R * 0.35, 0, Math.PI * 2);
  ctx.fill();
  // Spiral arms as a logarithmic point cloud.
  const rand = mulberry32(23);
  for (let arm = 0; arm < 2; arm++) {
    for (let i = 0; i < 900; i++) {
      const t = rand() * 3.2;
      const rr = R * 0.16 * Math.exp(0.35 * t);
      if (rr > R) continue;
      const angle = t * 2.2 + arm * Math.PI + (rand() - 0.5) * 0.5;
      const x = cx + Math.cos(angle) * rr * (1 + (rand() - 0.5) * 0.16);
      const y = cy + Math.sin(angle) * rr * 0.62 * (1 + (rand() - 0.5) * 0.16);
      ctx.fillStyle = rand() > 0.8 ? "rgba(138, 127, 212, 0.7)" : "rgba(200, 205, 216, 0.5)";
      ctx.fillRect(x, y, 1.3, 1.3);
    }
  }
  // "You are here" - the anchor the solar system shrinks into.
  const sx = cx + R * 0.42;
  const sy = cy + R * 0.12;
  ctx.strokeStyle = "#e8b45a";
  ctx.beginPath();
  ctx.arc(sx, sy, 5, 0, Math.PI * 2);
  ctx.stroke();
  if (alpha > 0.5 && R > 60) {
    ctx.fillStyle = "#e8b45a";
    ctx.font = "10px system-ui, sans-serif";
    ctx.fillText("Sun · you are here", sx + 8, sy + 3);
    ctx.fillStyle = "#9aa3b5";
    ctx.font = "11px system-ui, sans-serif";
    ctx.fillText("Milky Way", cx - 26, cy - R - 8);
  }
  ctx.globalAlpha = 1;
}

function drawCosmos(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  z: number,
  alpha: number,
  anchor: { x: number; y: number },
) {
  const rand = mulberry32(77);
  ctx.globalAlpha = alpha;
  // Filaments: soft random walks between galaxy clusters (the cosmic web).
  ctx.strokeStyle = "rgba(111, 212, 208, 0.10)";
  for (let f = 0; f < 26; f++) {
    ctx.beginPath();
    let x = rand() * w;
    let y = rand() * h;
    ctx.moveTo(x, y);
    for (let stepN = 0; stepN < 14; stepN++) {
      x += (rand() - 0.5) * w * 0.16;
      y += (rand() - 0.5) * h * 0.16;
      ctx.lineTo(x, y);
    }
    ctx.stroke();
  }
  // Galaxies
  for (let i = 0; i < 240; i++) {
    const x = rand() * w;
    const y = rand() * h;
    const r = rand() * 2.2 + 0.6;
    const hue = rand();
    ctx.fillStyle =
      hue > 0.75 ? "rgba(201, 107, 74, 0.6)" : hue > 0.4 ? "rgba(138, 127, 212, 0.55)" : "rgba(232, 228, 216, 0.5)";
    ctx.beginPath();
    ctx.ellipse(x, y, r * 1.6, r, rand() * Math.PI, 0, Math.PI * 2);
    ctx.fill();
  }
  // Marker on the Milky Way's home dot in the web.
  ctx.strokeStyle = "rgba(232, 180, 90, 0.7)";
  ctx.beginPath();
  ctx.arc(anchor.x, anchor.y, 6, 0, Math.PI * 2);
  ctx.stroke();
  if (alpha > 0.6) {
    ctx.fillStyle = "#e8b45a";
    ctx.font = "10px system-ui, sans-serif";
    ctx.fillText("Milky Way", anchor.x + 9, anchor.y + 3);
  }
  // Edge of the observable universe
  const eu = Math.min(w, h) * 0.52;
  ctx.strokeStyle = `rgba(111, 212, 208, ${0.35 * ramp(z, 3.1, 3.7, 99, 99)})`;
  ctx.setLineDash([2, 6]);
  ctx.beginPath();
  ctx.arc(w / 2, h / 2, eu, 0, Math.PI * 2);
  ctx.stroke();
  ctx.setLineDash([]);
  ctx.globalAlpha = 1;
}
