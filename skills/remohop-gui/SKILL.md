---
name: remohop-gui
description: Operate the graphical desktop of a remote machine by RemoHop ID with the `remohop` CLI - click, move the pointer, scroll, type text and press key combos, in a screenshot-act-verify loop. Use when a task on another computer can only be done through its GUI (a settings dialog, an installer, a desktop or browser app with no CLI), or the user asks you to click or type something on a remote screen. Needs a graphical session on the remote machine.
---

# RemoHop GUI control

`remohop <ID> click | move | scroll | type | press` act on the real desktop of the remote machine; a person there sees the pointer move and may be using it too. Requires a paired host (`remohop <ID> pair`; see `skills/README.md`), a graphical session on the host (Linux/X11, Windows or macOS; a macOS host needs the Accessibility permission for remohop) and the host's `input` feature (on by default).

If a shell command can do the job, use the `remohop-shell` skill instead: it is faster and far more reliable than driving a GUI.

(With the MCP server: `remohop_screenshot`, `remohop_click`, `remohop_move`, `remohop_scroll`, `remohop_type`, `remohop_key`. There, coordinates are pixels of the last screenshot image by default and are converted for you.)

## The loop: observe, act, verify

```bash
remohop <ID> screenshot -o /tmp/s1.png --max-width 1280 --json   # 1. observe; note "width" and "height" of the image
#    ... open /tmp/s1.png with your image viewer and find the target ...
remohop <ID> click 412 230 --scale-from 1280x720                 # 2. act (coordinates in that image)
remohop <ID> screenshot -o /tmp/s2.png --max-width 1280 --json   # 3. verify before the next action
```

One action per step. Never chain clicks blind: windows move, dialogs appear, pages load late.

## Coordinates - the part that goes wrong

X Y are pixels of the display **at full size**, origin 0,0 top-left. You usually look at a **downscaled** screenshot, so tell `remohop` the size of the image you measured in:

- `--scale-from WxH` - "my coordinates are in an image of this size showing the whole display". Use the exact `width`x`height` that `screenshot --json` printed (not the `--max-width` you asked for: a small screen is not upscaled).
- Without `--scale-from`, coordinates must be real display pixels (a screenshot taken without `--max-width`).
- Region screenshots (`-R X0,Y0,W,H`, in real display pixels, good for reading small text): image pixel (x,y) is display pixel (X0+x, Y0+y) when not downscaled. Add the offset yourself and click **without** `--scale-from`.
- Several monitors: `remohop <ID> screenshot --list`, then pass the same `-d N` (or name) to both `screenshot` and the input command.
- Aim at the centre of a button or field, not its edge. `--json` on any input command reports where it happened (`at`) and the final `pointer`.

## Commands

```bash
remohop <ID> click 640 360 --scale-from 1280x720          # left click
remohop <ID> click 640 360 --double --scale-from 1280x720 # double-click
remohop <ID> click 640 360 --right --scale-from 1280x720  # context menu (--middle also exists)
remohop <ID> move 800 12 --scale-from 1280x720            # hover (tooltips, menus)
remohop <ID> scroll 0 5                                   # 5 notches down (DX DY; negative = up/left; max 100)
remohop <ID> scroll 0 -3 --at 600,400 --scale-from 1280x720   # move there first: the window under the pointer scrolls
remohop <ID> type "hello world"                           # into whatever has keyboard focus
printf '%s' "$TEXT" | remohop <ID> type -                 # long or awkward text from stdin
remohop <ID> type -- "-starts-with-dash"
remohop <ID> press Return
remohop <ID> press ctrl+a Delete                          # combos in sequence
remohop <ID> press --repeat 5 Down
remohop <ID> press alt+F4
```

- `type` sends any Unicode regardless of the remote keyboard layout; a newline presses Return, a tab presses Tab. Click the field first and confirm focus in a screenshot. `--delay MS` slows typing for applications that drop characters.
- `press` combos are modifiers plus one key joined by `+`. Modifiers: `ctrl`, `shift`, `alt`, `super`, `altgr`. Keys include `Return`, `Tab`, `Escape`, `BackSpace`, `Delete`, `Home`, `End`, `Page_Up`, `Page_Down`, `Left`/`Right`/`Up`/`Down`, `space`, `F1`...; single characters name themselves. `remohop help press` lists them all. On a macOS host `super` (alias `cmd`) is Command and `alt` is Option: copy is `press super+c`, Spotlight `press super+space`.

## Rules

- **Never type passwords, tokens or other secrets.** They would pass through your transcript and sit on a screen that can be captured. Ask the user to enter credentials themselves, then continue.
- Clicks and shortcuts are as destructive as a person's: read dialogs before confirming, and say what you are about to do when it deletes, sends, buys or closes something.
- If the screen does not change after an action, do not repeat it blindly: take a screenshot, check focus and coordinates (most often a missing or wrong `--scale-from`).
- Errors: "no display available on the host" = headless machine or no graphical login; a message naming `remohop config feature input on` = the host's owner has switched input off (ask them); a view-only host refuses input; on macOS "has not granted this process the Accessibility permission" = the host's owner must allow remohop in System Settings > Privacy & Security > Accessibility (ask them; you cannot fix it remotely).
