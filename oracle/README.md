# Oracle corpus

This directory contains compatibility evidence, not NVDA runtime code.

Reference expectations come from NV Access's public NVDA 2026.1.1 system-test
assertions and source-level live-region contract at release commit
`5d92106f17e461dac62aa48257bbbf4183e033d0`. Each manifest case pins every
upstream file, file SHA-256, test or symbol name, and assertion identity. The
upstream test spy records logical speech sequences and raw braille buffer text
from a real NVDA process.

HooVDA observations must be captured independently inside the native Linux
container, from Chromium through AT-SPI and physical X11 gestures. Expected and
observed traces remain separate and immutable. `hoovda conformance` compares
command order, every asserted speech value, every asserted braille value, and
cursor mode. Speech-only upstream assertions do not pretend to prove braille.

The corpus is `complete` for its declared seven-case scope: every required
coverage tag and every `en-US`/`de-DE` by desktop/laptop matrix cell has pinned
upstream evidence. Semantic tags only count when attached to the required
audited upstream test or source symbol; labels alone cannot satisfy the gate.
The upstream behavior assertion is keyboard-layout neutral; each layout cell
must still have an independently captured Linux trace proving its physical
Insert or Caps Lock gesture. Localized expectations must pin NVDA's exact
official locale catalog. Never invent or manually translate missing output.

All four matrix cells pass the ARIA-details assertion. Browse quick-navigation,
NVDA's punctuation-filtered text-paragraph navigation, table entry and cached
axis output, and assertive live-region speech/braille also pass independent
Linux observations. Each de-DE case additionally pins
`source/locale/de/LC_MESSAGES/nvda.po`. Trace validation requires raw gesture
identity and the Linux Chromium/AT-SPI capture boundary.

`complete` means complete for this declared corpus. It does not claim every
NVDA application, add-on, Windows integration, synthesizer, braille display,
browser edge case, or document feature is implemented.

Upstream NVDA test assertions are licensed GPL-2.0-or-later. HooVDA remains
independent Apache-2.0 software; oracle evidence is kept as a separately
identified test-data corpus.
