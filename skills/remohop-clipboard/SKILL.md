---
name: remohop-clipboard
description: Read, set and transfer the clipboard of a remote machine by RemoHop ID with `remohop <ID> clip` - get what the user copied on another computer, inject text or an image into its clipboard, push/pull between machines, and export clipboard history as Markdown. Use when the user says "what I copied on <machine>", "put this on the other machine's clipboard", or wants clipboard content moved between computers.
---

# RemoHop clipboard

Cross-machine clipboard access for text and images. Requires a paired host (`remohop <ID> pair`; see `skills/README.md`).

(With the MCP server: `remohop_clipboard_get`, `remohop_clipboard_set`, `remohop_clipboard_history`.)

## Read the remote clipboard

```bash
remohop <ID> clip get                  # text to stdout
remohop <ID> clip get --json           # kind, size, hash, backend ("memory", "x11/xclip", ...)
remohop <ID> clip get -o /tmp/clip.png # an image must go to a file (or -o - for stdout)
```
`clip get` on an empty clipboard exits non-zero with a message; that is not a connection failure. If the clipboard holds an image and you called it without `-o`, use `--json` to see `kind`, then re-run with `-o FILE` and open the file with your image viewer.

## Set the remote clipboard

```bash
remohop <ID> clip set 'text to place on the remote clipboard'
printf '%s' "$SNIPPET" | remohop <ID> clip set          # from stdin (safe for multi-line / special chars)
remohop <ID> clip set --image /tmp/diagram.png          # PNG/JPEG/GIF, stored as PNG
```
This **overwrites** the remote clipboard; tell the user when you do it. Limit 32 MiB per item.

## Move between the two machines

```bash
remohop <ID> clip push        # local clipboard -> remote, once
remohop <ID> clip pull        # remote clipboard -> local, once
remohop <ID> clip -b          # continuous two-way sync in the background (see `remohop ps`, `remohop stop <PID>`)
remohop <ID> clip -b --pull-only
```
Reading or writing the **local** system clipboard needs a desktop session; in a headless agent environment `push`/`pull` may fail - use `get`/`set` with files or stdin instead.

## History and formatting

Everything that passed through the clipboard service on this machine is recorded (mode 0600, under the remohop config directory).

```bash
remohop <ID> clip history -n 5 --md          # Markdown for an LLM: one heading per entry, fenced text, image file paths
remohop <ID> clip history --since 2h --md
remohop <ID> clip history --all --json       # every peer, machine-readable
```
`--md` fences text with a fence longer than any backtick run inside it, so pasted code cannot break out. Images appear as file paths you can open. `--clear` deletes the history for that target - only on request.

## Headless hosts

A server without a desktop has no system clipboard: the host uses a **virtual in-memory clipboard** that lives as long as the `remohop host` process. `remohop <ID> clip status` (or `get --json`) reports the backend; `"memory"` means only RemoHop clients share it - a human at that machine cannot paste from it. Setting and reading it works normally for you.

## Etiquette

Clipboards often hold passwords and tokens. Read the remote clipboard only when the user asked for it, do not paste its content into logs or commits, and prefer reporting what it contained over storing it.
