# HooVDA

HooVDA is an independent, Linux-native screenreader runtime written in Go for
deterministic browser accessibility testing. It consumes real Chromium AT-SPI
objects and physical X11 keyboard events. It exposes structured speech,
braille, focus, mode, live-region and audio evidence to test harnesses.

HooVDA is independent software. It is not NVDA and is not affiliated with NV
Access. The immutable `nvda-web-2026.1.1` profile is a behavior-compatibility
target backed by pinned public system-test assertions, not an NVDA binary,
source port, or fork.

## Current release gate

No public release is permitted while `hoovda conformance` reports an incomplete
or mismatching oracle corpus. Reference expectations come from pinned official
NVDA 2026.1.1 system-test assertions; this repository contains no Windows
runtime, recorder, VM, Wine integration, NVDA executable, or NVDA source.

The conformance manifest pins the upstream release commit, capture tool, source
file, source test and assertion plus each fixture, expected reference trace,
and observed HooVDA trace by SHA-256. A case passes only when command, ordered
speech, ordered braille, and mode match at every step. Each trace must contain
both speech and braille. All four locale/layout cells are mandatory. Missing
files, changed hashes, path escapes, symlink escapes, unknown fields,
incomplete coverage, or any trace mismatch fail closed. See `oracle/README.md`.

Known parity gaps remain: math, complex embedded content, browser edge cases,
and exact speech or braille parity lack complete reference coverage. Character,
word, line and find navigation use a Unicode-rune cursor. Elements-list summary,
find-next/find-previous, automatic focus mode, report-details, table headers and
spans are implemented. Logical NVDA-style braille buffer text is kept separate
from Liblouis-translated display cells. Linux/amd64 Chromium and
`en-US`/`de-DE` are the only declared targets. Current code is a testable
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
