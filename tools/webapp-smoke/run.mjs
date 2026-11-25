import { parseArgs } from "node:util";
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { readFileSync, writeFileSync, mkdirSync, renameSync } from "node:fs";
import { dirname } from "node:path";
import { cases } from "./cases.mjs";
const { values: v } = parseArgs({
  options: Object.fromEntries(
    [
      "control",
      "cdp",
      "token-file",
      "label",
      "out",
      "apps",
      "playwright",
      "timeout-ms",
      "step-timeout-ms",
    ].map((k) => [k, { type: "string" }]),
  ),
});
for (const key of ["control", "cdp", "token-file", "label", "out"])
  if (!v[key]) throw Error("Missing --" + key);
for (const key of ["control", "cdp"])
  if (!["127.0.0.1", "localhost", "[::1]"].includes(new URL(v[key]).hostname))
    throw Error(
      "Only loopback endpoints are accepted; forward private remote ports over SSH.",
    );
const { chromium } = createRequire(import.meta.url)(
  v.playwright ?? "playwright-core",
);
const token = readFileSync(v["token-file"], "utf8")
  .trim()
  .replace(/^HST_CONTROL_TOKEN=/, "");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const api = async (
  path,
  body,
  timeout = Number(v["step-timeout-ms"] ?? 15000),
) => {
  const res = await fetch(v.control + path, {
    method: body ? "POST" : "GET",
    headers: {
      authorization: "Bearer " + token,
      "content-type": "application/json",
    },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(timeout),
  });
  const data = await res.json();
  if (!res.ok)
    throw Error(
      res.status +
        " " +
        path.split("/").at(-1) +
        " " +
        JSON.stringify(data).slice(0, 280),
    );
  return data;
};
const clamp = (x) =>
  String(x ?? "")
    .trim()
    .slice(0, 400);
const norm = (x) =>
  String(x ?? "")
    .normalize("NFKC")
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, " ")
    .trim();
const has = (xs, expected) =>
  !!norm(expected) && xs.some((x) => norm(x.text).includes(norm(expected)));
const err = (e) => clamp(e?.message ?? e);
const mini = (o) => (o ? { role: o.role, name: clamp(o.name) } : null);
function evidence(events = []) {
  const byKind = (kind) =>
    events
      .filter((e) => e.kind === kind)
      .map((e) => ({
        sequence: e.sequence,
        command: e.causalCommand,
        provenance: e.provenance,
        text: String(e.text ?? ""),
        ...(kind === "braille"
          ? { cells: e.brailleCells ?? null, cursor: e.brailleCursor ?? null }
          : {}),
      }));
  return {
    speech: byKind("speech"),
    braille: byKind("braille"),
    modeEvents: events.filter((e) => e.kind === "mode").map((e) => e.mode),
  };
}
function shaped(r) {
  return {
    ...evidence(r.events),
    mode: r.state?.cursor?.mode,
    focus: mini(r.state?.focus),
    browse: mini(r.state?.browse),
    physical: r.delivery,
    gesture: r.gesture,
    beforeSequence: r.beforeSequence,
    afterSequence: r.cursor,
    timedOut: r.timedOut,
  };
}
const browser = await chromium.connectOverCDP(v.cdp, { timeout: 12000 });
const sha = (bytes) => createHash("sha256").update(bytes).digest("hex");
const report = {
  schema: 2,
  sources: {
    runner: sha(readFileSync(new URL("./run.mjs", import.meta.url))),
    cases: sha(readFileSync(new URL("./cases.mjs", import.meta.url))),
  },
  label: v.label,
  started: new Date().toISOString(),
  health: await api("/v2/health"),
  browser: browser.version(),
  apps: [],
};
if (!report.health.ready) throw Error("Reader is not ready");
const selected = v.apps ? v.apps.split(",") : cases.map((x) => x.id);
if (selected.some((x) => !cases.some((c) => c.id === x)))
  throw Error("Unknown app selection " + selected);
mkdirSync(dirname(v.out), { recursive: true });
function save() {
  writeFileSync(v.out + ".tmp", JSON.stringify(report, null, 2) + "\n");
  renameSync(v.out + ".tmp", v.out);
}
const navTimeout = Number(v["timeout-ms"] ?? 18000);
async function testApp(c) {
  const app = { id: c.id, url: c.url, loaded: false, steps: [] },
    started = Date.now();
  report.apps.push(app);
  save();
  const context = await browser.newContext({
    locale: "en-US",
    viewport: null,
    acceptDownloads: false,
  });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(navTimeout);
  const cdp = await context.newCDPSession(page);
  let session;
  async function step(id, fn) {
    const s = { id, ok: false },
      start = Date.now();
    app.steps.push(s);
    try {
      await fn(s);
      if (!s.error) s.ok = true;
    } catch (e) {
      s.error = err(e);
    }
    s.durationMs = Date.now() - start;
    save();
    return s;
  }
  const action = async (command) => {
    const response = await api(session + "/actions", { command });
    if (
      response.command !== command ||
      response.delivery !== "physical" ||
      !response.gesture ||
      response.timedOut
    )
      throw Error(
        "Native command did not settle with its physical gesture: " +
          JSON.stringify({
            command: response.command,
            delivery: response.delivery,
            gesture: response.gesture,
            timedOut: response.timedOut,
          }),
      );
    return response;
  };
  const events = async (seq) =>
    (await api(session + "/events?after=" + seq + "&timeoutMs=900&quietMs=160"))
      .events ?? [];
  async function captureAfterActivate(s, actionResponse) {
    // The DOM may update before focus and AT-SPI notifications reach the reader.
    // Keep the immediate response intact, then sample without repeating the gesture.
    await sleep(2800);
    const settled = await api(
      session + "/events?after=" + actionResponse.cursor,
    );
    s.afterActivate = {
      windowMs: 2800,
      ...evidence(settled.events ?? []),
      state: mini((await api(session + "/state")).focus),
    };
  }
  const names = async () =>
    (await cdp.send("Accessibility.getFullAXTree")).nodes
      .filter((n) => !n.ignored && n.name?.value)
      .map((n) => ({ role: n.role?.value, name: clamp(n.name.value) }));
  async function inspectTarget(loc) {
    // Ask Chromium for the accessible name of this particular DOM element, including label/description associations.
    const key = "__hsSmokeLocator";
    await loc.evaluate((el, key) => {
      Object.defineProperty(window, key, { value: el, configurable: true });
    }, key);
    const remote = await cdp.send("Runtime.evaluate", {
      expression: "window." + key,
      objectGroup: "smoke",
    });
    try {
      const ax = await cdp.send("Accessibility.getPartialAXTree", {
        objectId: remote.result.objectId,
        fetchRelatives: false,
      });
      return {
        role: ax.nodes[0]?.role?.value,
        name: clamp(ax.nodes[0]?.name?.value),
      };
    } finally {
      await cdp.send("Runtime.releaseObjectGroup", { objectGroup: "smoke" });
      await page.evaluate((key) => delete window[key], key);
    }
  }
  try {
    await page.bringToFront();
    const response = await page.goto(c.url, {
      waitUntil: "domcontentloaded",
      timeout: navTimeout,
    });
    app.status = response?.status();
    app.finalUrl = page.url();
    app.title = await page.title();
    if (app.status === 200 && !app.title) {
      try {
        await page.waitForFunction(() => !!document.title, null, {
          timeout: 4500,
        });
      } catch {}
      app.title = await page.title();
    }
    if (!app.title)
      app.pageNotice = clamp(
        await page
          .locator("body")
          .innerText({ timeout: 1000 })
          .catch(() => ""),
      );
    if (
      !response ||
      app.status >= 400 ||
      !new RegExp(c.title, "i").test(app.title) ||
      new URL(page.url()).hostname !== new URL(c.url).hostname
    )
      throw Error("Unexpected public page: " + app.status + " " + app.title);
    const selector =
      c.input ?? c.link ?? c.todo ?? c.toggle ?? c.math ?? c.dialog;
    const loc = selector
      ? (c.math
          ? page.locator(selector)
          : page.locator(selector).filter({ visible: true })
        ).first()
      : null;
    if (loc)
      try {
        await loc.waitFor({
          state: c.math ? "attached" : "visible",
          timeout: 7000,
        });
      } catch {
        throw Error(
          "Expected public workflow target absent or hidden: " + selector,
        );
      }
    // The task target, title, status and hostname distinguish real content from redirects, login and error pages.
    app.loaded = true;
    const { id } = await api("/v2/sessions", {
      testId: "public-webapp-" + v.label + "-" + c.id,
      recording: false,
    });
    session = "/v2/sessions/" + id;
    await sleep(550);
    const snap = await names(),
      allHead = snap.filter((n) => n.role === "heading").map((n) => n.name),
      allLinks = snap.filter((n) => n.role === "link").map((n) => n.name);
    app.axSummary = {
      namedHeadings: allHead.length,
      namedLinks: allLinks.length,
    };
    const prep = await step("native-web-focus", async (s) => {
      let st = await api(session + "/state");
      let attempts = 0;
      const rightPage = (st) =>
        st.webContentFocused &&
        st.browserWindowActive &&
        (!st.focus?.documentUrlSha256 ||
          st.focus.documentUrlSha256 ===
            createHash("sha256").update(page.url()).digest("hex")) &&
        (!/document/i.test(st.focus?.role ?? "") ||
          !st.focus?.name ||
          st.focus.name === app.title);
      // A document switch can precede the corresponding AT-SPI event.
      for (let retry = 0; !rightPage(st) && retry < 6; retry++) {
        await sleep(280);
        st = await api(session + "/state");
      }
      while (!rightPage(st) && attempts++ < 3)
        st = (await action("returnToPage")).state;
      s.focus = mini(st.focus);
      s.mode = st.cursor?.mode;
      s.expectedKey = "web-document";
      s.attempts = attempts;
      s.nativeWindow = st.browserWindowActive;
      if (!rightPage(st))
        throw Error("Native focus did not identify the current web document");
      if (st.cursor?.mode === "focus") await action("toggleFocusMode");
    });
    if (prep.ok) {
      for (const [cmd, role, known] of [
        ["nextHeading", "heading", allHead],
        ["nextLink", "link", allLinks],
      ]) {
        if (!known.length) continue;
        await step(cmd, async (s) => {
          s.expectedKey = "page-" + role;
          const start = await action("documentStart");
          s.beforeMode = start.state?.cursor?.mode;
          // A page can autofocus its editor after the initial browse-mode request.
          // H/K are ordinary typed characters in focus mode. Confirm browse mode at delivery.
          let current = await api(session + "/state");
          if (current.cursor?.mode === "focus") {
            const toggle = await action("toggleFocusMode");
            s.modeSwitch = shaped(toggle);
            current = await api(session + "/state");
          }
          s.deliveryMode = current.cursor?.mode;
          if (s.deliveryMode !== "browse")
            throw Error("Browse mode did not settle before the navigation key");
          const focusedField = () =>
            page.evaluate(() => {
              const el = document.activeElement;
              return el instanceof HTMLInputElement ||
                el instanceof HTMLTextAreaElement
                ? String(el.value).slice(0, 100)
                : null;
            });
          s.fieldBefore = await focusedField();
          // Ctrl+Home can already place the cursor on the only heading/link. A subsequent H/K legitimately reports the end.
          const startsOnTarget =
            norm(start.state?.browse?.role) === role &&
            known.some((x) => norm(x) === norm(start.state?.browse?.name));
          let r = await action(
            startsOnTarget && known.length === 1 ? "readCurrent" : cmd,
          );
          if (
            cmd === "nextLink" &&
            c.id === "hackers-news" &&
            r.state?.browse?.role === "link" &&
            !known.some((x) => norm(x) === norm(r.state?.browse?.name))
          ) {
            const first = await inspectTarget(page.locator("a[href]").first());
            if (first.role === "link" && !first.name) {
              s.unnamedFirst = { chromium: first, reader: shaped(r) };
              r = await action("nextLink");
            }
          }
          s.operation = r.command;
          Object.assign(s, shaped(r));
          s.fieldAfter = await focusedField();
          if (
            s.fieldAfter !== s.fieldBefore &&
            s.fieldBefore !== null &&
            s.fieldAfter !== null
          )
            throw Error("Quick navigation typed into the focused form control");
          const active = r.state?.browse;
          const n =
            known.find((x) => norm(x) === norm(active?.name)) ??
            known
              .filter((x) => has(s.speech, x))
              .sort((a, b) => b.length - a.length)[0];
          if (!n || !has(s.speech, n) || !has(s.braille, n))
            throw Error(
              "Expected a current Chromium " +
                role +
                " name in both output channels",
            );
          s.expectedKey = n;
          s.expected = n;
        });
      }
    }
    if (c.tail) {
      await step("last-heading-from-document-end", async (s) => {
        const heading = page
          .getByRole("heading")
          .filter({ visible: true })
          .last();
        const expected = await inspectTarget(heading);
        s.expectedKey = expected.name;
        if (!norm(expected.name).includes(norm(c.tail)))
          throw Error(
            "RFC deployment no longer ends with its expected heading: " +
              expected.name,
          );
        const end = await action("documentEnd");
        s.end = shaped(end);
        const result = await action("previousHeading");
        Object.assign(s, shaped(result));
        if (!has(s.speech, expected.name) || !has(s.braille, expected.name))
          throw Error(
            "Previous heading from document end missed the final Chromium heading",
          );
      });
    }
    if (c.consent) {
      const dialog = page
        .getByRole("dialog", { name: /asks for your consent/i })
        .first();
      if (await dialog.isVisible()) {
        const controls = async () =>
          await dialog.getByRole("button").allTextContents();
        app.consent = { before: await controls() };
        let reject = dialog
          .getByRole("button", {
            name: /^(reject all|do not consent|necessary only|decline all|deny all)$/i,
          })
          .first();
        if (!(await reject.isVisible())) {
          const manage = dialog
            .getByRole("button", { name: /^manage options$/i })
            .first();
          if (await manage.isVisible()) {
            await manage.click();
            app.consent.afterManage = await controls();
          }
          reject = dialog
            .getByRole("button", {
              name: /^(reject all|do not consent|necessary only|decline all|deny all)$/i,
            })
            .first();
        }
        if (!(await reject.isVisible())) {
          app.loaded = false;
          throw Error(
            "Consent dialog blocks the workflow; no explicit reject-all or necessary-only option available",
          );
        }
        app.consent.choice = await reject.innerText();
        await reject.click();
        await dialog.waitFor({ state: "hidden", timeout: 4000 });
      }
    }
    if (loc) {
      const target = await inspectTarget(loc);
      app.target = target;
      if (await loc.evaluate((el) => el === document.activeElement)) {
        await loc.evaluate((el) => el.blur());
        await sleep(450);
      }
      await step("focus-target", async (s) => {
        s.expectedKey = [target.role, target.name].join(":");
        const before = (await api(session + "/state")).lastSequence;
        await loc.focus();
        if (!(await loc.evaluate((el) => document.activeElement === el)))
          throw Error("DOM focus did not reach selected target");
        const focusStart = Date.now();
        let ev = await events(before);
        Object.assign(s, evidence(ev));
        s.firstWindow = {
          ms: Date.now() - focusStart,
          speech: s.speech,
          braille: s.braille,
        };
        // Wait for delayed native AT-SPI focus without injecting focus a second time.
        if (
          target.name &&
          (!has(s.speech, target.name) || !has(s.braille, target.name))
        ) {
          do {
            await sleep(200);
            ev = (await api(session + "/events?after=" + before)).events ?? [];
            Object.assign(s, evidence(ev));
          } while (
            Date.now() - focusStart < 4500 &&
            (!has(s.speech, target.name) || !has(s.braille, target.name))
          );
        }
        s.observationMs = Date.now() - focusStart;
        const st = await api(session + "/state");
        s.focus = mini(st.focus);
        s.mode = st.cursor?.mode;
        if (!target.name) {
          s.error = "Target has no accessible name in Chromium";
          return;
        }
        if (!has(s.speech, target.name) || !has(s.braille, target.name))
          throw Error(
            "First focus did not present the Chromium accessible name in both channels",
          );
      });
      await step("read-target", async (s) => {
        s.expectedKey = [target.role, target.name].join(":");
        const r = await action("readCurrent");
        Object.assign(s, shaped(r));
        if (
          !target.name ||
          !has(s.speech, target.name) ||
          !has(s.braille, target.name)
        )
          throw Error(
            "Read current missed the Chromium accessible name in speech or braille",
          );
      });
      if (c.toggle) {
        await step("switch-drawing-tool", async (s) => {
          s.expectedKey = "Rectangle:pressed";
          s.before = await loc.getAttribute(c.attribute);
          if (s.before !== "false")
            throw Error("Expected an initially inactive drawing tool");
          const r = await action("activate");
          Object.assign(s, shaped(r));
          await page.waitForFunction(
            (sel) =>
              document.querySelector(sel)?.getAttribute("aria-pressed") ===
              "true",
            c.toggle,
            { timeout: 3500 },
          );
          s.actual = await loc.getAttribute(c.attribute);
          await captureAfterActivate(s, r);
        });
      } else if (c.math) {
        await step("calculate-expression", async (s) => {
          s.expectedKey = c.result;
          let st = await api(session + "/state");
          if (st.cursor?.mode === "browse")
            st = (await action("toggleFocusMode")).state;
          const before = st.lastSequence;
          await page.keyboard.type(c.text, { delay: 40 });
          await page
            .getByText(c.result, { exact: true })
            .first()
            .waitFor({ state: "attached", timeout: 4000 });
          s.actual = c.result;
          s.mode = st.cursor?.mode;
          Object.assign(s, evidence(await events(before)));
        });
      } else if (c.dialog) {
        await step("close-editor-dialog", async (s) => {
          s.expectedKey = "dialog dismissed";
          const r = await action("activate");
          Object.assign(s, shaped(r));
          await page
            .getByRole("dialog")
            .first()
            .waitFor({ state: "hidden", timeout: 4500 });
          s.actual = "dialog dismissed";
          await captureAfterActivate(s, r);
        });
      } else if (c.input || c.todo) {
        await step("type-into-app", async (s) => {
          s.expectedKey = c.text;
          let st = await api(session + "/state");
          if (st.cursor?.mode === "browse") {
            st = (await action("toggleFocusMode")).state;
          }
          const before = st.lastSequence;
          await loc.press("Control+a");
          await page.keyboard.type(c.text, { delay: 18 });
          await sleep(140);
          const actual = await loc.inputValue();
          s.expected = c.text;
          s.actual = actual;
          s.mode = st.cursor?.mode;
          Object.assign(s, evidence(await events(before)));
          if (actual !== c.text)
            throw Error(
              "Typed text did not reach the selected application field",
            );
        });
        if (c.count)
          await step("render-updated-count", async (s) => {
            s.expectedKey = c.count;
            await page
              .getByText(c.count, { exact: true })
              .first()
              .waitFor({ state: "visible" });
            s.actual = c.count;
          });
        if (c.todo)
          await step("commit-local-todo", async (s) => {
            s.expectedKey = c.text;
            const before = (await api(session + "/state")).lastSequence;
            await page.keyboard.press("Enter");
            await page
              .getByText(c.text, { exact: true })
              .first()
              .waitFor({ state: "visible" });
            Object.assign(s, evidence(await events(before)));
            s.actual = "New list item visible";
            s.mode = (await api(session + "/state")).cursor?.mode;
          });
        else
          await step("clear-query", async (s) => {
            s.expectedKey = "empty-query";
            const before = (await api(session + "/state")).lastSequence;
            await loc.press("Control+a");
            await page.keyboard.press("Backspace");
            s.actual = await loc.inputValue();
            Object.assign(s, evidence(await events(before)));
            s.mode = (await api(session + "/state")).cursor?.mode;
            if (s.actual !== "")
              throw Error("Clearing text did not restore the empty field");
          });
      } else if (c.link) {
        await step("activate-public-link", async (s) => {
          const href = await loc.getAttribute("href");
          const targetURL = new URL(href, page.url());
          s.expectedKey = targetURL.pathname + targetURL.hash;
          if (
            targetURL.origin !== new URL(c.url).origin ||
            !targetURL.href.includes(c.dest)
          )
            throw Error(
              "The deployed link no longer matches its declared read-only target",
            );
          const r = await action("activate");
          Object.assign(s, shaped(r));
          await page.waitForURL(
            (url) =>
              url.origin === targetURL.origin &&
              url.pathname === targetURL.pathname &&
              url.hash === targetURL.hash,
            { timeout: 7000, waitUntil: "commit" },
          );
          s.actualUrl = page.url();
          s.actualTitle = await page.title();
          await captureAfterActivate(s, r);
        });
      }
    } else if (c.key) {
      await step("display-pressed-key", async (s) => {
        s.expectedKey = c.shown;
        const before = (await api(session + "/state")).lastSequence;
        await page.keyboard.press(c.key);
        await page
          .getByText(c.shown, { exact: true })
          .first()
          .waitFor({ state: "visible" });
        Object.assign(s, evidence(await events(before)));
        s.actual = c.shown;
      });
    }
  } catch (e) {
    app.error = err(e);
  } finally {
    if (session)
      try {
        await api(session + "/finish", {}, 35000);
      } catch (e) {
        app.finalizeError = err(e);
      }
    await context.close().catch(() => {});
    app.durationMs = Date.now() - started;
    save();
    console.log(
      v.label,
      c.id,
      app.loaded ? "loaded" : "blocked",
      app.steps.filter((s) => s.ok).length + "/" + app.steps.length,
      app.error ?? app.finalizeError ?? "",
    );
  }
}
try {
  for (const id of selected) await testApp(cases.find((c) => c.id === id));
  report.finished = new Date().toISOString();
} finally {
  save();
  await browser.close();
}
