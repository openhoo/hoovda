import { readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
const norm = (x) =>
  String(x ?? "")
    .normalize("NFKC")
    .toLowerCase()
    .replace(/\s+/g, " ")
    .trim();
const cells = (s) =>
  (s.braille ?? []).map((x) => [x.cells ?? null, x.cursor ?? null]);
const equal = (a, b) => JSON.stringify(a) === JSON.stringify(b);
function payload(s, clean = (x) => x) {
  return [s.speech, s.braille].map((xs) =>
    (xs ?? []).map((x) => clean(x.text ?? "")),
  );
}
const stepsByID = (x) => new Map((x?.steps ?? []).map((s) => [s.id, s]));
function change(x, y, flags) {
  if (!x || !y) return "unpaired-step";
  if (typeof x.ok !== "boolean" || typeof y.ok !== "boolean")
    return "incomplete-step";
  const generic = (k) => ["page-link", "page-heading"].includes(k);
  if (
    x.expectedKey &&
    y.expectedKey &&
    x.expectedKey !== y.expectedKey &&
    !generic(x.expectedKey) &&
    !generic(y.expectedKey)
  )
    return "different-target";
  if (x.ok && !y.ok) return "regression-candidate";
  if (!x.ok && y.ok) return "improved";
  if (!x.ok && !y.ok) return "shared-gap";
  if (!flags.presentationEqual) return "presentation-difference";
  if (!flags.settledPresentationEqual)
    return "post-action-presentation-difference";
  if (!flags.cellsEqual) return "braille-cell-difference";
  if (!flags.modeEqual) return "mode-difference";
  if (!flags.deliveryEqual) return "delivery-difference";
  return "match";
}
export function validatePair(a, b) {
  if (!a.finished || !b.finished)
    throw Error(
      "Incomplete lane: an in-progress or aborted report is not a completed comparison",
    );
  if (
    a.schema !== b.schema ||
    a.sources?.runner !== b.sources?.runner ||
    a.sources?.cases !== b.sources?.cases ||
    a.sources?.target !== b.sources?.target
  )
    throw Error(
      "Runner schema, runner source or case manifest differs across lanes",
    );
  if (
    a.browser !== b.browser ||
    ["profile", "locale", "keyboardLayout"].some(
      (k) => a.health[k] !== b.health[k],
    )
  )
    throw Error("Browser/profile/locale/keyboard layout differ across lanes");
}
export function compare(a, b) {
  const rows = [];
  for (const id of new Set([
    ...a.apps.map((x) => x.id),
    ...b.apps.map((x) => x.id),
  ])) {
    const left = a.apps.find((x) => x.id === id),
      right = b.apps.find((x) => x.id === id);
    if (!left || !right || !left.loaded || !right.loaded) {
      rows.push({
        app: id,
        category: "unpaired-environment",
        left: left?.error ?? left?.status,
        right: right?.error ?? right?.status,
      });
      continue;
    }
    if (
      left.error ||
      right.error ||
      left.finalizeError ||
      right.finalizeError ||
      !left.steps?.length ||
      !right.steps?.length
    ) {
      rows.push({
        app: id,
        category: "incomplete-app",
        left:
          left.error ??
          left.finalizeError ??
          (!left.steps?.length ? "No steps" : "completed"),
        right:
          right.error ??
          right.finalizeError ??
          (!right.steps?.length ? "No steps" : "completed"),
      });
    }
    const l = stepsByID(left),
      r = stepsByID(right);
    for (const id2 of new Set([...l.keys(), ...r.keys()])) {
      const x = l.get(id2),
        y = r.get(id2);
      const flags =
        x && y
          ? {
              presentationEqual: equal(payload(x), payload(y)),
              normalizedPresentationEqual: equal(
                payload(x, norm),
                payload(y, norm),
              ),
              settledPresentationEqual: equal(
                payload(x.afterActivate ?? {}),
                payload(y.afterActivate ?? {}),
              ),
              cellsEqual:
                equal(cells(x), cells(y)) &&
                equal(
                  cells(x.afterActivate ?? {}),
                  cells(y.afterActivate ?? {}),
                ),
              modeEqual: x.mode === y.mode,
              deliveryEqual:
                x.physical === y.physical &&
                x.gesture === y.gesture &&
                x.deliveryMode === y.deliveryMode &&
                x.operation === y.operation,
            }
          : {};
      const category = change(x, y, flags);
      rows.push({
        app: id,
        step: id2,
        category,
        left: x?.error ?? x?.ok,
        right: y?.error ?? y?.ok,
        ...flags,
      });
    }
  }
  return {
    left: a.label,
    right: b.label,
    rows,
    counts: rows.reduce(
      (m, x) => ((m[x.category] = (m[x.category] ?? 0) + 1), m),
      {},
    ),
  };
}
if (
  process.argv[1] &&
  pathToFileURL(process.argv[1]).href === import.meta.url
) {
  const [a, b, out] = process.argv.slice(2);
  const inputs = [a, b].map((f) => JSON.parse(readFileSync(f, "utf8")));
  validatePair(...inputs);
  const result = compare(...inputs);
  writeFileSync(out, JSON.stringify(result, null, 2) + "\n");
  console.log(result.counts);
}
