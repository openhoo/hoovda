# HooVDA

HooVDA is a clean-room, Linux-native screenreader runtime written in Go for
deterministic browser accessibility testing. It consumes real Chromium AT-SPI
objects and physical X11 keyboard events. It exposes structured speech,
braille, focus, mode, live-region and audio evidence to test harnesses.

HooVDA is independent software. It is not NVDA and is not affiliated with NV
Access. The immutable `nvda-web-2026.1.1` profile is a black-box compatibility
target, not an NVDA binary or source port.

## Current release gate

No public release is permitted while `hoovda conformance` reports an incomplete
or mismatching oracle corpus. Reference traces must be supplied independently;
this repository contains no Windows runtime, recorder, VM, Wine integration,
NVDA executable, or NVDA source.

The conformance manifest pins each fixture, expected reference trace, and
observed HooVDA trace by SHA-256. A case passes only when command, ordered
speech, ordered braille, and mode match at every step. Missing files, changed
hashes, path escapes, symlink escapes, unknown fields, incomplete coverage, or
any trace mismatch fail closed.

Known parity gaps remain: elements-list and find dialogs are reserved but not
advertised; math, embedded content, browser edge cases, and exact speech or
braille parity lack independent reference coverage. Character, word, and line
navigation now use a Unicode-rune cursor. Table navigation now resolves AT-SPI
row and column headers, row and column spans, descriptions, and relationships,
but exact NVDA behavior remains gated on reference traces. Linux/amd64 Chromium
and `en-US`/`de-DE` are the only declared targets. Current code is a testable
foundation, not full NVDA parity.

## Local development

```bash
make generate
make test
make build
./bin/hoovda doctor
```

Runtime:

```bash
dbus-run-session -- env \
  DISPLAY=:99 \
  HOOVDA_CONTROL_TOKEN=development-only \
  ./bin/hoovda serve
```

## Locked profile

- HooVDA profile: `nvda-web-2026.1.1`
- accessibility API: AT-SPI 2.60.1 XML contract
- transport: `github.com/godbus/dbus/v5` 5.2.2
- speech synthesis: eSpeak NG 1.52 through external driver process
- braille translation: Liblouis 3.38.0 through external driver process
- locales: `en-US`, `de-DE`
- target: Linux/amd64 Chromium website content and stock browser chrome

The runtime image includes exact source archives and license texts for pinned
AT-SPI, eSpeak NG, and Liblouis builds. See `THIRD_PARTY_NOTICES.md`.
