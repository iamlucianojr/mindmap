# One-shot prompt — build the mindmap tool

Paste everything below the line into a fresh agent session in an empty directory.

---

Build a local, single-user mindmap tool for planning software work. Go backend,
one HTML file for the UI, no dependencies outside the standard library, no npm,
no build step. It runs on `127.0.0.1:4455` and serves one document.

I care more about the decisions below being respected than about the tool being
finished. Where a decision is given with a reason, keep the reason in a comment.

## What it is for

A map of work I draw myself. A node starts as an idea, becomes a thing that
needs doing, and can be handed to whoever or whatever does it. The map is also
something I can ask questions about while I draw.

## The document

**Mermaid `mindmap` text is the only format.** Not a wrapper around it, not an
export of it — the document *is* mermaid, so any other renderer still opens it.
Parse it on load, serialise it on save.

**The log is append-only.** One JSON object per line: `{n, at, note, src}`.
Never overwrite, never compact. `tail` must be a valid debugger. The revision
number is **where a line sits in the log**, not a field a line claims about
itself.

## Work orders

A node can carry one, as a trailing tag on its label so the file stays valid
mermaid:

```
Rewrite the importer @wo(a3f91c2b,doing,backend,ABC-12)
```

- The **id comes first and never changes**. Minted client-side, 8 hex
  characters. Renaming or moving the node must not orphan what hangs off it.
- An id is always 8 hex and a stage name never is, so a legacy `@wo(open)` tag
  written before ids existed still parses correctly. Detect on shape, not
  position.
- Everything a label *can* hold stays in the document, so undo covers it.
- The **description is the one thing too long for a label.** It lives in
  `orders/<id>.md`. Fetched when the panel opens, never cached, so a second
  window cannot show a stale one.

The stages a piece of work moves through, in order. Make the list a single
constant — whoever uses this will have their own:

```
open → specified → doing → checking → reviewing → ready → done
```

A node renders **one slot per stage**, each `done` / `running` / `stopped` /
`empty`, drawn to the right of its label. Not a word, not a pill: the question
a map of work answers is *where is this*, and a row of dots answers it at a
glance. A stop is red and belongs to the stage that was running.

Put the derivation behind one function that returns only those four words.
Everything downstream speaks them, so the day the stage is read from real
evidence rather than typed, that one function changes and nothing else does.

Emitting a work order writes a brief file and records the order in its own
append-only log. **Anything the generator cannot know is written `<UNKNOWN>`,
never guessed.** A generator that invents a value is the same failure as an
agent inventing one, a step earlier.

## Asking questions

A question is a job **about** a node, not part of the document. No tag, no
mermaid mutation — so undo stays about the map and an answer is never lost to
an unrelated edit. Key jobs by the node's path from the root.

Five properties, all load-bearing:

1. **The job is written to disk before it runs.** A goroutine dies with the
   process; a line in a file does not. On boot, anything still marked `running`
   was interrupted — re-queue it. At-least-once is the right failure direction:
   asking twice is harmless, losing the answer is not.
2. **The job id is `sha256(path + prompt)[:12]`.** The same question on the same
   node is the same job. Reword it and it is honestly a different question.
3. **Closing the browser cancels nothing.** The job's context comes from the
   process, never from `r.Context()`. Cancelling a *wait* must never cancel the
   *work*. This is one line and it is the line everybody gets wrong.
4. **`POST /api/ask` returns 202 with an id immediately.** It never waits. That
   single property is what the rest of the design rests on.
5. **The prompt carries the path from the root.** "What does this mean?" on a
   node called *the third option* is unanswerable; with its ancestors above it,
   it is not.

Stream progress to the page over **SSE**, not WebSocket and not polling.
Traffic is one-way and the browser reconnects by itself.

The model sits behind an interface with a **fake implementation as the
default** — instant, free, and the queue is entirely real, so the tool can be
developed without credentials or waiting. A real adapter shells out to a CLI
agent, because that re-reads whatever credentials exist rather than holding a
token that expires.

Add a `Kind` field to the job now. Running a piece of work is the same thing as
asking a question — a longer prompt and a file to write — so it can join this
queue later without a second one being invented. One field now, a rewrite later.

## Backend shape

One package is fine; separate by file.

```
main.go    flags, wiring, graceful shutdown
doc.go     the document log
ask.go     Job, queue, worker pool — no HTTP, no vendor
model.go   the Model interface: fake, and one real adapter
order.go   descriptions and brief generation
http.go    handlers and the SSE hub
```

The queue must not know which model it has, and must not import `net/http`. If
it does, the design has slipped.

Serve the page **from disk**, not embedded, so editing the UI needs no rebuild.

Endpoints:

```
GET  /                    the page
GET  /api/doc             latest revision
POST /api/doc             save
GET  /api/history         revisions, without the source
GET  /api/order/{id}      description
POST /api/order/{id}      set description
POST /api/workorder       emit a brief
GET  /api/ask             every job, newest first
POST /api/ask             202 {id}
GET  /api/ask/{id}        one job
GET  /api/events          SSE
```

An order id arrives from the page and becomes a path. **Validate it is an id** —
alphanumeric, dash, underscore, bounded length — or it can name any file on the
machine.

## The UI

Hand-drawn SVG. Curved branches, a colour per top-level branch inherited down.

**Depth must be readable without counting edges.** A wide type scale and
*falling* contrast — the trunk bright, the leaves muted. Everything the same
weight is the single biggest thing that makes a large map unreadable.

**Selecting a node dims everything not on its line** — its ancestors and its
subtree stay at full contrast, the rest drops to about 44%. On a hundred nodes
this is the difference between reading a branch and hunting for it. Not lower:
dimmed should mean quieter, not gone.

**No floating toolbar.** A right-click context menu, each row showing its
keyboard shortcut, which is how anyone actually learns them. It acts on the
node you aimed at, selecting it first.

Keyboard, following MindMeister where it is better:

```
tab / enter    child / sibling        ⇧enter   sibling above
dbl-click      rename                 del      delete
⌘↑ ⌘↓          reorder siblings       arrows   navigate
⌥1 ⌥2 ⌥9       show one, two, all     0 / F6   reset zoom / recentre
space          advance the stage      t        open the work order
w              emit the brief         a        ask about this node
⌘z / ⌘⇧z       undo / redo            ⌘f       find
?              the full list
```

Deliberately diverge from MindMeister on `space`: in a work map you stage far
more often than you collapse, and collapsing already has knobs.

Also: an outline view of the same tree, editable, sharing the same functions as
the map. Export png, svg, mermaid, and **markdown with `- [ ]` checkboxes** — a
map that cannot be pasted into a ticket stays on one machine.

The header reads `saved 4 min ago · 23 versions`. An ISO timestamp is a
machine's answer to "when"; a person wants to know whether what they just typed
is safe.

## Traps — each of these cost me real time

**`setPointerCapture` on `pointerdown` retargets the subsequent `click` to the
element that captured.** Any new chrome drawn inside the canvas silently stops
receiving clicks. I fixed it four times by adding to an exclusion list, and the
list was the bug — anything that forgot to join it broke silently. Invert it:

```js
if (!(e.target === stage || e.target.closest("#svg"))) return;
```

Grab only when the press actually landed on the canvas. Also defer the capture
until the pointer has moved ~4px, so a click that starts on a draggable surface
is still a click.

**Node ids must be assigned by the document, not by the layout pass.** I
numbered them inside the map's layout, so the outline view addressed nodes that
had never been numbered and every edit silently missed.

**Clicking a label in the outline must not re-render the list.** It destroys the
`contenteditable` the caret just entered and the first keystroke goes nowhere.

**A new blank node is dropped by the parser if you serialise before it has a
label.** Do not round-trip the document until the label exists.

**The collapse knob of a parent sits immediately left of its children**, so
status badges go to the *right* of the label. Left collides with every one.

**Size the canvas to include what hangs off a node**, not just the labels, or
anything drawn beside a node is clipped.

**Do not reference a variable before its `const`.** I wrote a badge that read a
later-declared variable: it threw only for the nodes that had a badge, which is
exactly when it should have appeared, and the SVG silently kept its previous
contents so everything looked fine.

## How to know it works

Write tests, and **mutate each one before trusting it**. A test that passes when
the behaviour is broken is worse than no test. Specifically prove:

- a job survives `kill -9` mid-answer and finishes after a restart
- the same question twice returns one job id
- a finished job is not re-run on boot
- a brief refuses to be written with no repo, and names `<UNKNOWN>` with no
  description
- an order id cannot escape its directory
- the document log survives reopening and reports the right revision count

For the UI, drive a real browser. **Assert an element is visible before reading
a value out of it** — I had a test pass against a hidden panel because focus had
stayed in a textarea, the shortcut never fired, and the assertion happily read
stale DOM. And never assert a DOM index when ids exist.

## Shape of the answer

Working code, `go build` clean, `go vet` clean, `gofmt` clean, tests passing
with `-race`. A README that says how to run it, which is the thing READMEs
always leave out.
