# mindmap

A map of work you draw yourself, that can answer questions about itself.

    go build -o mindmap . && ./mindmap

Then http://127.0.0.1:4455

No flags needed. `-model pi` swaps the fake answerer for a real agent,
`-addr` moves the port, `-dir` points it at a different set of files.

## The document

One mermaid `mindmap` file, kept in an append-only log, so any other renderer
still opens it and nothing you typed is ever overwritten.

A work order rides on a node's label as a trailing tag:

    Register the namespace @wo(a3f91c2b,building,flux,PAY-59)

The id comes first and never changes, so renaming or moving the node does not
orphan what hangs off it. Everything a label *can* hold stays in the document,
so undo covers it. The description is the one thing too long for a label, and
lives in `orders/<id>.md`.

## Asking

A question is a job *about* a node, not part of the document. It never touches
the mermaid text, so undo stays about the map and an answer is never lost to an
unrelated edit.

Jobs are keyed by the node's path from the root, which is what the server
hashes into the job id. Ask the same thing twice and it is one job.

    -model fake     answers instantly and for free, for working on the tool
    -model claude   shells out to the claude CLI, which reads the credentials
                    already on this machine rather than holding a token

## Keys

    tab / enter        child / sibling          ⇧enter   sibling above
    dbl-click          rename                   del      delete
    ⌘↑ ⌘↓              reorder                  arrows   navigate
    ⌥1 ⌥2 ⌥9           show one, two, all       0 / F6   reset / recentre
    space              advance the stage        t        open the work order
    w                  emit the brief           a        ask about this node
    ⌘z / ⌘⇧z           undo / redo              ⌘f       find
    right-click        everything, with its shortcut beside it
    ?                  the full list

## Files

    mindmap.log.jsonl   every revision of the map
    jobs.log.jsonl      every question and its state transitions
    orders/<id>.md      what a work order actually asks for

All three are append-only or plain text. `tail` is a valid debugger.
