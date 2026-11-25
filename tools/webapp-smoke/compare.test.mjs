import { test } from "node:test";
import assert from "node:assert/strict";
import { compare, validatePair } from "./compare.mjs";
const report = (load, steps) => ({
  apps: [{ id: "app", loaded: load, steps }],
});
test("does not count two unreachable apps as a match", () =>
  assert.equal(
    compare(report(false, []), report(false, [])).rows[0].category,
    "unpaired-environment",
  ));
test("separates semantic regressions, changed live targets, missing and literal variations", () => {
  const step = (id, ok, expectedKey, s) => ({
    id,
    ok,
    expectedKey,
    speech: [{ text: s }],
  });
  const a = [
    step("a", true, "edit", "Search box"),
    step("b", true, "book", "Book"),
    step("c", true, "link", "here"),
    step("d", true, "x", "one"),
  ];
  const b = [
    step("a", false, "edit", ""),
    step("b", true, "new book", "New book"),
    step("c", true, "link", "here link"),
  ];
  assert.deepEqual(
    compare(report(true, a), report(true, b)).rows.map((x) => x.category),
    [
      "regression-candidate",
      "different-target",
      "presentation-difference",
      "unpaired-step",
    ],
  );
});

test("a missing heading announcement on an unchanged page is not counted as changed live copy", () => {
  const a = { id: "nextHeading", ok: true, expectedKey: "A real heading" },
    b = { id: "nextHeading", ok: false, expectedKey: "page-heading" };
  assert.equal(
    compare(report(true, [a]), report(true, [b])).rows[0].category,
    "regression-candidate",
  );
});

test("identical printed braille does not hide a changed physical cell or cursor", () => {
  const s = {
    id: "read",
    ok: true,
    braille: [{ text: "same", cells: "AA==", cursor: 0 }],
  };
  const t = { ...s, braille: [{ text: "same", cells: "AQ==", cursor: 0 }] };
  assert.equal(
    compare(report(true, [s]), report(true, [t])).rows[0].category,
    "braille-cell-difference",
  );
});

test("a loaded app with no completed checks or failed session finalization stays incomplete", () => {
  const a = report(true, [{ id: "test", ok: true }]);
  const b = report(true, []);
  b.apps[0].finalizeError = "session timed out";
  const [app, missing] = compare(a, b).rows;
  assert.equal(app.category, "incomplete-app");
  assert.equal(app.right, "session timed out");
  assert.equal(missing.category, "unpaired-step");
});
test("literal differences survive normalization and missing delivery is observable", () => {
  const a = {
    id: "read",
    ok: true,
    speech: [{ text: "Name  Here" }],
    physical: "physical",
    gesture: "h",
  };
  const b = { ...a, speech: [{ text: "name here" }] };
  const row = compare(report(true, [a]), report(true, [b])).rows[0];
  assert.equal(row.category, "presentation-difference");
  assert.equal(row.normalizedPresentationEqual, true);
  const c = { ...a, physical: undefined };
  assert.equal(
    compare(report(true, [a]), report(true, [c])).rows[0].category,
    "delivery-difference",
  );
});
test("checks source and manifest independently of browser identity before comparing", () => {
  const a = {
    schema: 2,
    finished: "now",
    browser: "Chromium",
    health: { profile: "desktop" },
    sources: { runner: "one", cases: "two" },
  };
  assert.doesNotThrow(() => validatePair(a, { ...a }));
  for (const [k, v] of [
    ["runner", "old"],
    ["cases", "other"],
    ["target", "other"],
  ]) {
    assert.throws(
      () => validatePair(a, { ...a, sources: { ...a.sources, [k]: v } }),
      /source or case manifest/,
    );
  }
  assert.throws(
    () => validatePair(a, { ...a, finished: null }),
    /Incomplete lane/,
  );
});

test("a timeout before target identification cannot manufacture a different live target", () => {
  const a = { id: "native-web-focus", ok: false, error: "timeout" };
  const b = { id: "native-web-focus", ok: true, expectedKey: "web-document" };
  assert.equal(
    compare(report(true, [a]), report(true, [b])).rows[0].category,
    "improved",
  );
});
test("post-action speech or braille is compared when both immediate responses were silent", () => {
  const a = {
    id: "activate",
    ok: true,
    afterActivate: { speech: [{ text: "Document" }] },
  };
  const b = { id: "activate", ok: true, afterActivate: { speech: [] } };
  assert.equal(
    compare(report(true, [a]), report(true, [b])).rows[0].category,
    "post-action-presentation-difference",
  );
});
