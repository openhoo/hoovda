# Public web application smoke comparison

The runner connects to an existing, **dedicated** Chromium and HooVDA v2 runtime. Each app gets a new browser context and reader session. It makes anonymous visits, uses public read-only links, and types sample text in search fields or local-only workflows. It does not log into sites or submit messages. Changing pages may change what two runs observe.

Use a maintained Node release with built-in fetch, and Playwright Core matching your Chromium. Pass an installed package path with `--playwright` or install `playwright-core` where Node resolves it. Forward each remote control/CDP pair to loopback and supply the bearer token in a private file. Run each lane on its own desktop:

```sh
node run.mjs --label original --control http://127.0.0.1:53161 --cdp http://127.0.0.1:53162 --token-file /path/to/control-token --out /path/to/original.json
node run.mjs --label candidate --control http://127.0.0.1:53163 --cdp http://127.0.0.1:53164 --token-file /path/to/control-token --out /path/to/candidate.json
node compare.mjs /path/to/original.json /path/to/candidate.json /path/to/comparison.json
node --test compare.test.mjs
```

`--apps wikipedia,todomvc` selects deployments for targeted rechecks. `--timeout-ms` sets the navigation deadline (18 seconds by default); `--step-timeout-ms` sets the reader HTTP deadline (15 seconds). Use a new output file for each rerun. The runner writes incremental JSON, but only a report with `finished` set is a completed run. The runner only closes browser contexts it created.

For each site the runner verifies its title, top-level HTTP status, hostname and public task target. For quick navigation it checks speech and braille against accessible names obtained from Chromium's accessibility tree. Some sites have no headings; those cases record the tree counts and omit the heading step. Ctrl+Home can already put the reader on the only target; in that case the test reads that target and records the actual operation. On Hacker News, the first home link wraps an unnamed logo; the runner retains its presentation and checks the next named link.

The focus check uses Chromium to focus one identified element and observes native HooVDA events from a sequence checkpoint. This is distinct from navigating the entire site using Tab. Text edits use browser keyboard input. Public link activation uses the reader’s native command and verifies the resulting URL. Local state changes are verified in the application. Empty reader output for a browser text edit is retained but is not required to prove that the application accepted the edit.

The comparator requires the same runner and case manifest when source hashes are available. It separates unavailable or incomplete sites, different targets, shared failures, newly failing or newly passing semantic checks, and literal speech, braille, mode and physical gesture differences. Each paired row reports literal text equality and a second comparison that ignores case and extra spaces. Each channel preserves event order. Speech records are text from the reader, not audio recordings. Dynamic content, delayed events, and site accessibility problems require inspection before attributing a change to the engine. Both engines run on Linux; these results alone do not establish parity with Windows NVDA.

The runner records the browser and reader identity and hashes its own source and case manifest. For final comparisons it rechecks native browse mode immediately before a heading or link command and records the focused field's value before and after. Focus observation retains both the first event window and a bounded follow-up for delayed speech/braille; it never repeats the focus gesture while waiting. For RFC 9110 there is an explicit end-of-document traversal that checks the final heading in both channels. Braille events retain logical text, captured cell bytes (base64), and cursor when supplied by the engine. A comparator match does not imply acoustic or Windows-NVDA equivalence.

For activation gestures, the runner retains the immediate action response and separately observes the next 2.8 seconds after the browser confirms the effect. Native focus or document announcements may arrive after the HTTP response; inspect both windows.

## Measuring a repeatable link focus delay

The focused-link probe keeps one Lobsters page open in each isolated desktop. It alternates lane order across five rounds and establishes the same “Comments” → “Recent” starting state before each focus. Each round records the browser's focus dispatch, native event types, source object IDs, timestamps on the reader's monotonic clock, and the client's observation intervals. It does not issue reader state requests during the measured operation. Client receipt times include browser control, polling, and network overhead; native time deltas between focus, speech and braille do not.

```sh
node focus-latency.mjs --original http://127.0.0.1:53161 --original-cdp http://127.0.0.1:53162 --candidate http://127.0.0.1:53163 --candidate-cdp http://127.0.0.1:53164 --token-file /path/to/control-token --out /path/to/focus-latency.json
```

### Longer TodoMVC workflow

Use `todo-flow.mjs` with the same `--control`, `--cdp`, `--token-file`, `--label`, `--out` and optional `--playwright` arguments. It opens a fresh context on the public JavaScript ES6 demo and checks eight steps: create two tasks, edit one, check it, switch to Active, switch to Completed, clear completed, return to All, and reload. The check uses the site's double-click editor, keyboard input, and physical reader commands to read and activate controls. It verifies application state, native speech and braille for the checked state and named controls, and records the Chromium accessibility tree. This ES6 deployment stores the tasks in memory: the reload step expects it to reset. Its item checkboxes lack accessible names; the test checks their state without inventing a name.
