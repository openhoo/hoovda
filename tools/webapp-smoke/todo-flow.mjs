import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import fs from "node:fs";
import { createRequire } from "node:module";
import { parseArgs } from "node:util";

const { values: opts } = parseArgs({
  options: Object.fromEntries(
    ["control", "cdp", "token-file", "out", "label", "playwright"].map((k) => [
      k,
      { type: "string" },
    ]),
  ),
});
for (const k of ["control", "cdp", "token-file", "out", "label"])
  assert.ok(opts[k], "Missing --" + k);
for (const k of ["control", "cdp"])
  assert.ok(
    ["127.0.0.1", "localhost", "[::1]"].includes(new URL(opts[k]).hostname),
  );
const key = fs
  .readFileSync(opts["token-file"], "utf8")
  .trim()
  .replace(/^HST_CONTROL_TOKEN=/, "");
async function api(route, body) {
  const r = await fetch(opts.control + route, {
    method: body ? "POST" : "GET",
    headers: {
      authorization: "Bearer " + key,
      "content-type": "application/json",
    },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(25000),
  });
  assert.ok(r.ok, r.status + " " + route);
  return r.json();
}
const { chromium } = createRequire(import.meta.url)(
  opts.playwright ?? "playwright-core",
);
const b = await chromium.connectOverCDP(opts.cdp),
  c = await b.newContext({ locale: "en-US", acceptDownloads: false });
const p = await c.newPage();
p.setDefaultTimeout(5500);
const CDP = await c.newCDPSession(p);
const url = "https://todomvc.com/examples/javascript-es6/dist/";
const report = {
  label: opts.label,
  source: createHash("sha256")
    .update(fs.readFileSync(new URL(import.meta.url)))
    .digest("hex"),
  url,
  started: new Date().toISOString(),
  browser: b.version(),
  health: await api("/v2/health"),
  steps: [],
};
const write = () =>
  fs.writeFileSync(opts.out, JSON.stringify(report, null, 2) + "\n");
const saveEvents = (ev) =>
  ev
    .filter((e) =>
      ["speech", "braille", "mode", "commandSettled"].includes(e.kind),
    )
    .map(
      ({
        kind,
        text,
        brailleCells,
        brailleCursor,
        mode,
        causalCommand,
        reason,
        provenance,
      }) => ({
        kind,
        text,
        brailleCells,
        brailleCursor,
        mode,
        causalCommand,
        reason,
        provenance,
      }),
    );
const compact = (st) => ({
  focus: st.focus && {
    role: st.focus.role,
    name: st.focus.name,
    value: st.focus.value,
  },
  mode: st.cursor?.mode,
  webContentFocused: st.webContentFocused,
});
let sid;
async function ensure(condition, message) {
  const end = Date.now() + 5500;
  do {
    if (await condition()) return;
    await new Promise((r) => setTimeout(r, 110));
  } while (Date.now() < end);
  throw Error(message);
}
async function command(s, cmd) {
  const r = await api(sid + "/actions", { command: cmd });
  s.actions ??= [];
  const row = {
    command: cmd,
    delivery: r.delivery,
    gesture: r.gesture,
    timedOut: r.timedOut,
    state: compact(r.state),
    events: saveEvents(r.events),
  };
  s.actions.push(row);
  assert.ok(
    row.delivery === "physical" &&
      !row.timedOut &&
      row.events.some(
        (e) => e.kind === "commandSettled" && e.reason === "completed",
      ),
    "Native command did not settle: " + cmd,
  );
  return row;
}
const rows = p.locator(".todo-list li");
const labels = () => rows.locator(".view label").allTextContents();
async function expectLabels(want) {
  await ensure(
    async () => JSON.stringify(await labels()) === JSON.stringify(want),
    "Expected visible application list: " + want,
  );
}
async function focus(loc, role, name) {
  await loc.focus();
  await ensure(
    async () => {
      const st = await api(sid + "/state");
      return (
        st.webContentFocused &&
        new RegExp(role, "i").test(st.focus?.role ?? "") &&
        (!name || st.focus?.name === name)
      );
    },
    "Native focus did not reach " + [role, name].join(" "),
  );
}
async function read(s, checks) {
  const a = await command(s, "readCurrent");
  for (const [channel, regex] of Object.entries(checks)) {
    const text = a.events
      .filter((e) => e.kind === channel)
      .map((e) => e.text)
      .join(" ");
    assert.match(text, regex);
  }
}
async function snapshot(s) {
  const tree = (await CDP.send("Accessibility.getFullAXTree")).nodes;
  s.ax = tree
    .filter(
      (n) =>
        !n.ignored &&
        ["checkbox", "button", "link", "textbox"].includes(n.role?.value),
    )
    .map((n) => ({
      role: n.role.value,
      name: n.name?.value,
      checked: n.properties?.find((v) => v.name === "checked")?.value.value,
    }))
    .slice(0, 30);
  s.items = await labels();
  s.count = await p.locator(".todo-count").textContent();
  s.url = p.url();
}
async function step(id, fn) {
  const s = { id };
  report.steps.push(s);
  try {
    await fn(s);
    s.ok = true;
  } catch (e) {
    s.error = e.message;
    s.ok = false;
  }
  try {
    await snapshot(s);
  } catch (e) {
    s.snapshotError = e.message;
  }
  write();
}
const first = "Buy fresh pears",
  second = "Return library book",
  edited = "Buy green pears";
try {
  const r = await p.goto(url, { waitUntil: "domcontentloaded" });
  assert.equal(r.status(), 200);
  assert.match(await p.title(), /TodoMVC/);
  await p.bringToFront();
  sid =
    "/v2/sessions/" +
    (
      await api("/v2/sessions", {
        testId: "deep-public-todo-" + opts.label,
        recording: false,
      })
    ).id;
  const input = p.locator(".new-todo");
  await step("add-two-tasks", async (s) => {
    // The demo autofocuses before this reader session opens. Generate a real focus transition in each lane.
    if (await input.evaluate((el) => document.activeElement === el)) {
      await input.evaluate((el) => el.blur());
      await p.waitForTimeout(300);
    }
    await focus(input, "entry|text", "What needs to be done?");
    await read(s, {
      speech: /What needs to be done/i,
      braille: /What needs to be done/i,
    });
    for (const text of [first, second]) {
      await input.pressSequentially(text);
      await input.press("Enter");
    }
    await expectLabels([second, first]);
  });
  await step("edit-one-task", async () => {
    const row = rows.filter({ hasText: first });
    await row.locator("label").dblclick();
    const edit = rows.locator(".edit");
    await edit.press("Control+a");
    await edit.pressSequentially(edited);
    await edit.press("Enter");
    await expectLabels([second, edited]);
  });
  await step("check-one-task", async (s) => {
    const box = rows.filter({ hasText: edited }).getByRole("checkbox");
    await focus(box, "check");
    await box.press("Space");
    await ensure(() => box.isChecked(), "Checkbox did not toggle");
    await focus(box, "check");
    await read(s, { speech: /^check box\s+checked$/i, braille: /^chk ⣏⣿⣹$/ });
    assert.equal(await p.locator(".todo-count").innerText(), "1 item left");
  });
  async function filter(s, name, want) {
    const link = p.getByRole("link", { name, exact: true });
    await focus(link, "link", name);
    await read(s, {
      speech: new RegExp(name, "i"),
      braille: new RegExp(name, "i"),
    });
    await command(s, "activate");
    await expectLabels(want);
    assert.equal(await link.getAttribute("class"), "selected");
  }
  await step("active-filter", (s) => filter(s, "Active", [second]));
  await step("completed-filter", (s) => filter(s, "Completed", [edited]));
  await step("clear-completed", async (s) => {
    const btn = p.getByRole("button", { name: /clear completed/i });
    await focus(btn, "button", "Clear completed");
    await read(s, { speech: /Clear completed/i, braille: /Clear completed/i });
    await command(s, "activate");
    await expectLabels([]);
  });
  await step("return-to-all", (s) => filter(s, "All", [second]));
  // This in-memory ES6 deployment clears the list on reload (verified in a separate browser-only context).
  await step("reload-resets-local-demo", async () => {
    await p.reload({ waitUntil: "domcontentloaded" });
    await expectLabels([]);
    assert.equal(await p.locator(".todo-count").textContent(), "0 items left");
  });
} catch (e) {
  report.error = e.message;
} finally {
  if (sid)
    try {
      await api(sid + "/finish", {});
    } catch (e) {
      report.finishError = e.message;
    }
  await c.close();
  await b.close();
}
report.finished = new Date().toISOString();
report.passed =
  !report.error &&
  !report.finishError &&
  report.steps.length === 8 &&
  report.steps.every((s) => s.ok);
write();
console.log(
  report.label,
  report.passed,
  report.error ?? "",
  report.steps.map((x) => [x.id, x.ok, x.error ?? ""]),
);
if (!report.passed) process.exitCode = 1;
