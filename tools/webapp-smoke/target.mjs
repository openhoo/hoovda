const fold = (s) => String(s ?? "").normalize("NFKC").toLowerCase().trim();
export const norm = (s) => fold(s).replace(/[^\p{L}\p{M}\p{N}]+/gu, " ").trim();

export function hasName(events, name) {
  const words = norm(name);
  if (!words) {
    const symbol = fold(name);
    return !!symbol && events.some((e) => fold(e.text).split(/\s+/u).includes(symbol));
  }
  return events.some((e) => (" " + norm(e.text) + " ").includes(" " + words + " "));
}

// Chromium AX and the native AT-SPI interface name some roles differently.
const nativeRoles = {
  textbox: ["textbox", "entry", "text", "password text"],
  searchbox: ["searchbox", "entry", "text"],
  button: ["button", "push button", "toggle button"],
};

export function matchesFocus(target, focus) {
  const name = (s) => fold(s).replace(/\s+/gu, " ");
  const role = name(target?.role);
  return !!role && !!name(target?.name) &&
    name(target.name) === name(focus?.name) &&
    (Object.hasOwn(nativeRoles, role) ? nativeRoles[role] : [role]).includes(name(focus?.role));
}
