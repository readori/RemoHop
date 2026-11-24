---
name: remohop-screenshot
description: Capture the screen of a remote machine by RemoHop ID with `remohop <ID> screenshot` so you can look at the real UI - an error dialog, a GUI app state, a browser page, a login screen. Use when the user asks what is on another computer's screen, when a remote GUI problem cannot be diagnosed from logs, or when you need visual confirmation of a remote change. Works with multi-monitor machines; fails clearly on headless hosts.
---

# RemoHop screenshot

Takes a screenshot on the remote machine, optionally downscales it there, and saves it locally so you can read it with your own image/file viewer. Requires a paired host (`remohop <ID> pair`; see `skills/README.md`).

(With the MCP server: `remohop_screenshot` returns the image directly as an image content block, `remohop_displays` lists monitors.)

## Capture, then look

```bash
remohop <ID> screenshot -o /tmp/screen.png
```
Then **open `/tmp/screen.png` with your image-viewing capability** (for example the Read tool on an image path, or attach it to the conversation). The command only writes the file; you have not seen anything until you view it.

Handy options:

```bash
remohop <ID> screenshot -o /tmp/screen.png --max-width 1568   # downscale on the remote side: faster, cheaper for vision models
remohop <ID> screenshot -o /tmp/screen.jpg --format jpeg --quality 70   # smaller file (.jpg extension also selects jpeg)
remohop <ID> screenshot -o /tmp/screen.png --json              # {"path","width","height","format","bytes","display"}
remohop <ID> screenshot -o - > /tmp/screen.png                 # stream the bytes to stdout (summary goes to stderr)
```
Default output name without `-o` is `remohop-<target>-<timestamp>.png` in the current directory - always pass `-o` so you know where it went. Use a fresh path per capture if you want to compare before/after.

## Multiple monitors

```bash
remohop <ID> screenshot --list              # index, geometry, which is primary
remohop <ID> screenshot --list --json
remohop <ID> screenshot -o /tmp/second.png --display 1     # -d 1 or a name from --list also work
remohop <ID> screenshot -o /tmp/all.png --display all     # the whole desktop in one image
```
Display `0` is the primary monitor.

## Choosing the size

`--max-width 1568` suits most vision models. Reading small text (logs in a terminal, dense dialogs): raise it (`2400`) or capture just the monitor you need; a very wide `--display all` image downscaled to 1568 may be unreadable.

A region reads small text without a huge image: `remohop <ID> screenshot -o /tmp/part.png -R 0,0,800,600` (X,Y,W,H in real display pixels; image pixel (0,0) is display pixel (X,Y)).

To act on what you see (click, type), use the `remohop-gui` skill.

## Errors

- **"no display available on the host: ..."** - the remote machine is headless, or no one is logged in to a graphical session (or the host runs as a service outside the desktop session). `remohop <ID> info` shows `display` yes/no. A screenshot is impossible there; inspect via logs and commands instead, or ask the user to start `remohop host` inside their desktop session.
- **"no display N (the host has M)"** - wrong index; run `--list`.
- A message naming `remohop config feature screen on` - the host's owner has switched screen capture off; ask them, do not retry.
- Exit 255 with a pairing/credentials message - ask the user to run `remohop <ID> pair`.

## Habits

Screenshots can contain private information (mail, chats, secrets). Capture only when it serves the task, keep files in `/tmp`, and delete them when done. Describe what you see and its relevance rather than transcribing everything on screen.
