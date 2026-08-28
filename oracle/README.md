# Oracle corpus

This directory contains compatibility evidence, not NVDA runtime code.

Reference expectations come from NV Access's public NVDA 2026.1.1 system-test
assertions at release commit `5d92106f17e461dac62aa48257bbbf4183e033d0`.
Each manifest case pins the exact upstream file, file SHA-256, test name, and
assertion identity. The upstream test spy records logical speech sequences and
raw braille buffer text from a real NVDA process.

HooVDA observations must be captured independently inside the native Linux
container, from Chromium through AT-SPI and physical X11 gestures. Expected and
observed traces remain separate and immutable. `hoovda conformance` compares
command order, every speech value, every braille value, and cursor mode.

The corpus stays `incomplete` until every required coverage tag and every
`en-US`/`de-DE` by desktop/laptop matrix cell has pinned upstream evidence.
Never infer or translate missing expected output. Never mark the corpus
complete by duplicating a different locale or keyboard-layout trace.

Upstream NVDA test assertions are licensed GPL-2.0-or-later. HooVDA remains
independent Apache-2.0 software; oracle evidence is kept as a separately
identified test-data corpus.
