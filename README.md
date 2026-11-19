# HooVDA

HooVDA is an independent, Linux-native screenreader runtime written in Go for
deterministic browser accessibility testing. It consumes real Chromium AT-SPI
objects and physical X11 keyboard events. It exposes structured speech,
braille, focus, mode, live-region and audio evidence to test harnesses.

HooVDA is independent software. It is not NVDA and is not affiliated with NV
Access. The immutable `nvda-web-2026.1.1` profile is a behavior-compatibility
target backed by pinned public system-test assertions, not an NVDA binary,
source port, or fork.

Braille previous/next line follows the text caret in a focused editor. When
Chromium exposes a separate accessible block for each line, the editor's
embedded-object offset identifies the block; offsets within its text refer
to that child. A successful move updates the native caret before presenting
the target line. In review mode, moving the braille cursor leaves the editor's
caret in place. A background document finishing its load does not supersede
an object that still has native focus.

## Testing the first announcement in a dialog

Set an output checkpoint before activating the control that opens a dialog, then
inspect the **native focus** speech after that checkpoint. On first focus,
HooVDA presents the dialog’s accessible name and description (when enabled)
alongside the focused control. Tabbing to a second control inside the same
dialog should announce the new control once without repeating the dialog.
A subsequent read command has its own output and cannot establish what was
announced automatically.

Chromium can deliver focus while a full AT-SPI tree refresh is still timing out.
HooVDA can recover the focused control’s parent path from live AT-SPI links to
the cached browser tree. It uses that path for presentation only: the last
complete tree remains available for navigation. A cycle, disconnected parent
or link to another application does not establish a dialog context. This
behavior is covered by synthetic native-event tests; the separate locked
profile below describes what is checked against NVDA reference evidence.

## Locked-profile release gate

`hoovda conformance` now passes the seven-case declared corpus. Reference
expectations come from pinned official NVDA 2026.1.1 system-test assertions and
one source-level live-region contract; this repository contains no Windows
runtime, recorder, VM, Wine integration, NVDA executable, or copied NVDA source.

The conformance manifest pins the upstream release commit, capture tool, source
file, source test and assertion plus each fixture, expected reference trace,
and observed HooVDA trace by SHA-256. de-DE cases also pin NVDA's official
localization catalog. A case passes only when command, ordered speech, ordered
braille, and mode match at every asserted step; raw HooVDA evidence must name
the exact physical gesture and Linux Chromium/AT-SPI capture boundary. Each
trace must contain every output channel declared by its tags. All four
locale/layout cells are mandatory.
Missing files, changed hashes, path escapes, symlink escapes, unknown fields,
incomplete coverage, unsubstantiated feature tags, or any trace mismatch fail
closed. See `oracle/README.md`.

Known parity gaps remain: math, complex embedded content, browser edge cases,
and exact speech or braille parity lack complete reference coverage. Character,
word, line and find navigation use a Unicode-rune cursor. Elements-list summary,
find-next/find-previous, automatic focus mode, report-details, table headers and
spans are implemented. Logical NVDA-style braille buffer text is kept separate
from Liblouis-translated display cells. Linux/amd64 Chromium and
`en-US`/`de-DE` are the only declared targets. The passing corpus proves only
its declared browser cases. Current code remains a compatibility subset, not
full NVDA parity.

## Release artifacts

Each versioned GitHub release includes a Linux/amd64 engine binary and
`SHA256SUMS`. Download both to the same directory. On Linux, run
`sha256sum -c SHA256SUMS`, `chmod +x hoovda-linux-amd64`, then
`./hoovda-linux-amd64 version` to inspect the embedded version and full source
revision. The binary needs the runtime services listed below to run a reader
session. Companion projects can build the complete runtime from this repo’s
Dockerfile at that revision; the image revision label and the running engine’s
`version` output should both match the pin.

To reproduce the release binary, use the Go version in `go.mod`, check out the
clean release tag, and run `tools/release-binary.sh vMAJOR.MINOR.PATCH` with
that tag. It writes the binary and checksum under `dist/<tag>/` and derives
the embedded build date from the commit.

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
