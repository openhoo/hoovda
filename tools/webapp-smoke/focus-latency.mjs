import { createRequire } from "node:module";
import { readFileSync, writeFileSync, renameSync } from "node:fs";
import { parseArgs } from "node:util";
const { values: v } = parseArgs({
  options: Object.fromEntries(
    [
      "original",
      "candidate",
      "original-cdp",
      "candidate-cdp",
      "token-file",
      "out",
      "playwright",
    ].map((k) => [k, { type: "string" }]),
  ),
});
for (const k of ["original", "candidate", "original-cdp", "candidate-cdp"]) {
  if (!["127.0.0.1", "localhost", "[::1]"].includes(new URL(v[k]).hostname))
    throw Error("Forward dedicated private endpoints to loopback");
}
const { chromium } = createRequire(import.meta.url)(
  v.playwright ?? "playwright-core",
);
const token = readFileSync(v["token-file"], "utf8")
  .trim()
  .replace(/^HST_CONTROL_TOKEN=/, "");
const url = "https://lobste.rs/",
  selector = 'a[href="/recent"]',
  name = "Recent",
  sleep = (ms) => new Promise((r) => setTimeout(r, ms)),
  clock = () => performance.now();
const data = {
  site: url,
  name,
  selector,
  started: new Date().toISOString(),
  method:
    "Alternating lane order by round, first native focus/speech/braille for a single DOM focus call. Snapshot polling has no server quiet interval. A different link is focused and confirmed in both channels before each trial.",
  lanes: {},
  trials: [],
};
function save() {
  writeFileSync(v.out + ".tmp", JSON.stringify(data, null, 2) + "\n");
  renameSync(v.out + ".tmp", v.out);
}
const live = new Map();
async function setup(k) {
  const b = await chromium.connectOverCDP(v[k + "-cdp"]);
  const ctx = await b.newContext({ locale: "en-US", viewport: null });
  const page = await ctx.newPage();
  const api = async (path, body) => {
    const r = await fetch(v[k] + path, {
      method: body ? "POST" : "GET",
      headers: {
        authorization: "Bearer " + token,
        "content-type": "application/json",
      },
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(18000),
    });
    if (!r.ok) throw Error(path.split("?")[0] + " " + r.status);
    return r.json();
  };
  const lane = { b, ctx, page, api };
  live.set(k, lane);
  await page.bringToFront();
  const res = await page.goto(url, {
    waitUntil: "domcontentloaded",
    timeout: 18000,
  });
  if (res.status() !== 200 || !/(^|\s)Lobsters/.test(await page.title()))
    throw Error("Lobsters unavailable: " + res.status());
  const target = page.locator(selector).filter({ visible: true }).first();
  if (!(await target.innerText()).trim().match(/^Recent$/i))
    throw Error("Target changed");
  const alternate = page
    .getByRole("link", { name: "Comments", exact: true })
    .first();
  await alternate.waitFor({ state: "visible", timeout: 4000 });
  const se = await api("/v2/sessions", {
    testId: "public-link-latency-" + k,
    recording: false,
  });
  const path = "/v2/sessions/" + se.id;
  Object.assign(lane, { path, target, alternate });
  data.lanes[k] = {
    health: await api("/v2/health"),
    browser: b.version(),
    title: await page.title(),
    alternate: await alternate.innerText(),
  };
  // Warm the same loaded document before trials.
  await api(path + "/state");
  return lane;
}
const narrow = (e) => ({
  seq: e.sequence,
  ns: e.monotonicNs,
  kind: e.kind,
  text: e.text ?? "",
  reason: e.reason ?? null,
  src: e.source ?? null,
  causalCommand: e.causalCommand ?? null,
  provenance: e.provenance ?? null,
});
async function focus(lane, loc, expected) {
  const { api, path } = lane,
    checkpoint = (await api(path + "/events")).cursor;
  if (await loc.evaluate((el) => el === document.activeElement))
    throw Error("No focus change: reset target was already active");
  const t0 = clock();
  const browserPromise = loc.evaluate((el) => {
    let eventMs = null;
    const a = performance.now();
    el.addEventListener("focus", () => (eventMs = performance.now() - a), {
      once: true,
    });
    el.focus();
    return {
      active: el === document.activeElement,
      dispatchMs: performance.now() - a,
      eventMs,
    };
  });
  const seen = new Map(),
    polls = [];
  let cursor = checkpoint,
    end = null;
  do {
    const send = clock() - t0;
    const batch = await api(path + "/events?after=" + cursor);
    const receipt = clock() - t0;
    polls.push([
      Math.round(send * 100) / 100,
      Math.round(receipt * 100) / 100,
      batch.cursor,
    ]);
    for (const raw of batch.events ?? [])
      seen.set(raw.sequence, { ...narrow(raw), receiptMs: receipt });
    cursor = batch.cursor;
    const es = [...seen.values()],
      focus = es.find(
        (e) =>
          e.kind === "focus" && e.text.toLowerCase() === expected.toLowerCase(),
      );
    const match = (kind) =>
      es.find(
        (e) =>
          e.kind === kind &&
          e.text.toLowerCase().includes(expected.toLowerCase()) &&
          JSON.stringify(e.src) === JSON.stringify(focus?.src),
      );
    const speech = match("speech"),
      braille = match("braille");
    if (focus && speech && braille) {
      end = {
        focusReceiptMs: focus.receiptMs,
        speechReceiptMs: speech.receiptMs,
        brailleReceiptMs: braille.receiptMs,
        focusToSpeechMs: (speech.ns - focus.ns) / 1e6,
        focusToBrailleMs: (braille.ns - focus.ns) / 1e6,
      };
      break;
    }
    await sleep(12);
  } while (clock() - t0 < 6500);
  const dom = await browserPromise;
  return { checkpoint, dom, result: end, events: [...seen.values()], polls };
}
try {
  await Promise.all(["original", "candidate"].map(setup));
  for (let i = 0; i < 5; i++)
    for (const k of i % 2
      ? ["candidate", "original"]
      : ["original", "candidate"]) {
      const lane = live.get(k),
        reset = await focus(lane, lane.alternate, "Comments");
      if (!reset.result || !reset.dom.active)
        throw Error(
          k +
            " reset to Comments failed: " +
            JSON.stringify(reset.events).slice(0, 500),
        );
      await sleep(350);
      const r = await focus(lane, lane.target, name);
      data.trials.push({ round: i + 1, lane: k, ...r });
      save();
      console.log(k, i + 1, JSON.stringify(r.result));
    }
  data.finished = new Date().toISOString();
  save();
} finally {
  await Promise.allSettled(
    [...live.values()].map(async ({ b, ctx, api, path }) => {
      try {
        if (path) await api(path + "/finish", {});
      } finally {
        await ctx.close();
        await b.close();
      }
    }),
  );
}
