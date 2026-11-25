import { test } from "node:test";
import assert from "node:assert/strict";
import { hasName, matchesFocus } from "./target.mjs";

test("RFC 9110 title cannot stand in for the focused link named 1", () => {
  const target = { role: "link", name: "1" };
  assert.equal(hasName([{ text: "RFC 9110: HTTP Semantics doc" }], "1"), false);
  for (const text of ["1  link", "1 lnk"])
    assert.equal(hasName([{ text }], "1"), true);
  assert.equal(matchesFocus(target, null), false);
  assert.equal(matchesFocus(target, { role: "document web", name: "1" }), false);
  assert.equal(matchesFocus(target, target), true);
});

test("matches native editable roles and whole Unicode phrases", () => {
  assert.equal(matchesFocus({ name: "Search", role: "searchbox" },
    { name: "Search", role: "entry" }), true);
  assert.equal(hasName([{ text: "Öffnen: PDF-Datei, Schalter" }], "PDF-Datei"), true);
  assert.equal(hasName([{ text: "research results" }], "search"), false);
  assert.equal(hasName([{ text: "link" }], ""), false);
  assert.equal(hasName([{ text: "+ button" }], "+"), true);
  assert.equal(hasName([{ text: "C++ button" }], "+"), false);
  assert.equal(matchesFocus({ name: "<head>", role: "link" },
    { name: "head", role: "link" }), false);
});
