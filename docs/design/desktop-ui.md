# Desktop app: design system

The visual design of the RemoHop desktop app (`internal/desktop/assets`), and
the frame the team edition's screens are drawn in. The app's behaviour and
architecture are in [desktop-app.md](../desktop-app.md); the user guide is
[en/desktop-app.md](../en/desktop-app.md).

The page runs in the system's web view (WebView2, WKWebView, WebKitGTK),
offline, under a strict Content-Security-Policy. So: system fonts only, no
web fonts, no CDN, no framework, no inline styles. Everything below is plain
CSS custom properties and vanilla JavaScript.

- [Principles](#principles)
- [Tokens](#tokens)
- [Layout](#layout)
- [Components](#components)
- [Screens: personal edition](#screens-personal-edition)
- [Screens: team edition](#screens-team-edition)
- [Accessibility](#accessibility)
- [Working on the UI](#working-on-the-ui)

## Principles

1. **Calm by default, loud when it matters.** A light neutral canvas, white
   cards with hairline borders, one accent colour. Colour is spent on state:
   green for on and connected, amber for waiting on you, red for off,
   refusals and anything that ends a session.
2. **The number first.** IDs, passwords and counts are set large, in tabular
   figures, so they can be read out over a phone line. Labels are small
   monospace capitals beside them.
3. **Never invent data.** A chart shows only what the app saw. A minute the
   app did not see is drawn as "not recorded", never as zero. A place with
   nothing to show has an empty state that says what will appear and how.
4. **Decisions are approval cards.** Anything that waits on the user (a
   connection request, a request for more access, in the team edition an
   access request) is a card with who, what, from where, the time left and
   two buttons: the safe one first and focused.
5. **Desktop, not phone.** Built for windows from about 800 x 560 to a full
   large screen, mouse and keyboard first, at the platform's text size (14 px
   body). It still works at 480 px wide.
6. **Words with every colour.** A status dot always sits beside its words; a
   chip always has a label.

## Tokens

All tokens are custom properties on `:root` in `style.css`. Dark values
apply under `prefers-color-scheme: dark` unless Settings chose light, and
under `html[data-theme="dark"]`. Dark mode is its own set of values, checked
against its own surfaces, not an inversion.

### Colour

| Token | Light | Dark | Use |
|---|---|---|---|
| `--bg` | `#f3f4f6` | `#0c0e12` | canvas, sidebar |
| `--card` | `#ffffff` | `#15181d` | cards, dialogs, selected sidebar item |
| `--card-2` | `#f7f8fa` | `#1b1f26` | the content frame, insets (credentials, table head) |
| `--hover` | `#eef0f3` | `#20252c` | hover, segmented tray, neutral counts |
| `--line` | `#e4e7ec` | `#262b33` | hairlines: card borders, dividers |
| `--line-2` | `#d0d5dd` | `#363d48` | control borders |
| `--text` | `#101318` | `#eceef2` | text |
| `--text-2` | `#4f5563` | `#aab0bb` | secondary text |
| `--text-3` | `#667085` | `#9198a4` | micro-labels, placeholders, axis labels |
| `--acc` | `#2546d8` | `#3f5ef0` | the accent: primary buttons, switch on, selection (RemoHop cobalt, the icon's colour) |
| `--acc-text` | `#2140c4` | `#a3b4ff` | links and text buttons |
| `--acc-soft` / `--acc-line` | `#eaeefd` / `#c7d1f8` | `#1b2447` / `#2d3b78` | help boxes, pressed chips, avatars |
| `--ok` / `--ok-fill` / `--ok-soft` | `#12754a` / `#1f9d63` / `#e4f4ec` | `#5ccf96` / `#23a26a` / `#10281c` | on, connected, online |
| `--warn` / `--warn-fill` / `--warn-soft` | `#8a5800` / `#dc9b00` / `#fcf2da` | `#f3bc45` / `#d29500` / `#2d2410` | waiting, needs you, connecting |
| `--bad` / `--bad-fill` / `--bad-soft` | `#c0261c` / `#d92d20` / `#fdecea` | `#ff8a80` / `#cc3329` / `#3a1714` | off, new device, scam warning, disconnect |
| `--dm-0` ... `--dm-4` | `#eceef2` `#c9d3fa` `#93a6f3` `#5b78e6` `#2546d8` | `#21262e` `#26346a` `#3b56c4` `#6582f2` `#a3b4ff` | dot-matrix steps: empty, then one hue light to dark |

Each status has three roles: a text colour (`--ok`), a fill for marks and
solid buttons (`--ok-fill`) and a tint for backgrounds (`--ok-soft`). Text
on a tint uses the text colour. Measured contrast (WCAG 2.1): every text
pair is at least 4.5:1, for example `--text-3` on `--card` 4.97:1 (light)
and 6.13:1 (dark), white on `--acc` 7.1:1 and 5.2:1, white on `--bad-fill`
4.8:1 and 5.2:1, `--warn` on `--warn-soft` 5.4:1 and 8.8:1.

The team edition adds no colours. Roles and policy states reuse these (an
admin chip is the accent, "expires soon" is amber, "revoked" is red).

### Type

System fonts: `"Segoe UI Variable Text", "Segoe UI", system-ui,
-apple-system, "PingFang SC", "Microsoft YaHei UI", "Noto Sans CJK SC", ...`
and for figures and labels `ui-monospace, "SF Mono", "Cascadia Mono",
Menlo, Consolas, ...`. Semibold (600) for emphasis, never bold body text.

| Role | Size / line | Weight | Notes |
|---|---|---|---|
| Page title | 26 / 32 | 600 | letter-spacing -0.015em |
| Wizard title | 28 / 36 | 600 | |
| Credential (ID, password) | 32 / 40 mono | 600 | tabular, `user-select: all` |
| KPI value | 34 / 40 | 600 | tabular, letter-spacing -0.02em |
| Card title | 15 / 22 | 600 | |
| Section title | 16 / 24 | 600 | |
| Body | 14 / 20 | 400 | |
| Small | 12 / 16 | 400 | notes, sub-lines |
| Micro-label | 11 / 16 mono, uppercase | 400-500 | letter-spacing 0.06em: eyebrows, column heads, settings groups, sidebar sections |

### Space, radius, elevation, motion

- **Spacing** on a 4 px grid: 4, 8, 12, 16, 24, 32. Card padding 16 x 18;
  gaps between cards 16; page padding 24 x 28 (16 below 720 px).
- **Radius**: `--r-1` 6 (small buttons in rows), `--r-2` 8 (controls,
  sidebar items, tiles), `--r-3` 12 (insets, list cards, large buttons),
  `--r-4` 16 (cards, dialogs, the content frame), 20 (wizard card),
  `--r-pill` (chips, counts, segmented controls, switches).
- **Elevation**: `--sh-1` (a 1 px shadow under cards and selected items),
  `--sh-2` (the wizard), `--sh-3` (dialogs, the snackbar). Borders do most
  of the work; shadows only lift.
- **Motion**: 150 ms on switches; the waiting dot pulses. Everything is off
  under `prefers-reduced-motion: reduce`.

## Layout

```
┌──────────────┬──────────────────────────────────────────────────────────┐
│ ▣ RemoHop  ◫ │ ┌ update bar (only when a release is waiting) ─────────┐ │
│              │ └──────────────────────────────────────────────────────┘ │
│ MAIN MENU    │ ╭────────────────── content frame ─────────────────────╮ │
│ ⌂ Home       │ │ EYEBROW                                              │ │
│ ⇥ Connect  6 │ │ Page title                                           │ │
│ ▭ Sessions 2●│ │ Lead line                                            │ │
│              │ │                                                      │ │
│ OPEN COMPUTERS │ cards ...                                            │ │
│ [S] Studio ⧉×│ │                                                      │ │
│              │ │                                                      │ │
│──────────────│ │                                                      │ │
│ ⚙ Settings   │ │                                                      │ │
│ ┌──────────┐ │ │                                                      │ │
│ │▭• This   │ │ │                                                      │ │
│ │ 104 713…│ │ │                                                      │ │
│ └──────────┘ │ ╰──────────────────────────────────────────────────────╯ │
└──────────────┴──────────────────────────────────────────────────────────┘
   240 px                    the rest; pages max 1240 px wide
```

- **Shell**: the sidebar on the canvas, the page in a framed panel
  (`--card-2`, 16 px radius, 8 px from the window edge) with cards inside.
  A computer that is open fills the frame with its console.
- **Rail**: below 900 px, or by the button beside the logo (remembered on
  this computer), the sidebar is a 68 px column of icons; names become
  tooltips, counts become a dot, open computers become their initial with
  the connection dot, and their close button appears on hover or focus.
- **Breakpoints** (window width): 1180 (Home's four-column grid), 1100 (the
  Sessions two-column grid, Settings' index), 900 (rail), 760 (the address
  book drops its System and Last seen columns), 720 (tighter page padding),
  560 (credentials stack).

## Components

**Sidebar item** (`.tab`). 36 px high, 8 px radius, an 18 px line icon, the
label, then an optional count pill or badge dot. Selected: card background,
hairline border, `--sh-1`, semibold, `aria-current="page"`. Section labels
are micro-labels. The foot holds Settings and the **this-computer card**
(icon with a presence dot, name, the ID or "Not reachable").

**Page header** (`.ph`). A micro-label eyebrow (Home: the date and time),
the page title, a lead line in `--text-2`.

**Card** (`.card`). White, hairline border, 16 px radius, `--sh-1`. Header
row (`.card-h`): an optional 32 px **tile** (neutral inset with a line
icon), the title, then controls at the end. An optional footer (`.card-f`)
carries links to where the details are, right-aligned, separated by a
hairline.

**KPI card** (`.kpi`). Status dot and label; the value large; a sub-line;
a footer link ("Sessions ->"). The dot is grey at zero and takes its
status colour when the value needs attention.

```
┌ ● Connected now ─────┐
│ 2                    │
│ Control 1 · Files 0  │
├──────────────────────┤
│           Sessions → │
└──────────────────────┘
```

**Status line** (`.status`). An 8 px dot and semibold small text in the
status colour: ok (with a soft halo), wait (the dot pulses), bad.

**Chip** (`.chip`). Pill, 24 px, small semibold. Variants: plain, `ok`,
`warn`, `bad`; toggles (`button.chip[aria-pressed]`) show a leading dot and
the accent tint when on (the View / Control / Files access of a session or
a remembered device).

**Count** (`.count`). A small pill with a number: neutral, `warn` (waiting
requests), `ok` (live sessions).

**Segmented control** (`.seg.pill`). Pills in a tinted tray; the pressed
one is a white pill with `--sh-1` (language, theme, the address book's All
/ Online / Paired).

**Switch** (`.switch`). 40 x 22 pill, accent when on. The **on/off switch**
(`.switch.onoff`) is red while off and green while on, for a state that
must be readable at a glance: unattended access.

**Buttons** (`.btn`). 34 px, 8 px radius, semibold: primary (accent),
`tonal` (white, bordered), `outline`, `text` (accent text), `danger` (red
fill), sizes `sm` (30) and `lg` (44, the Connect page). Keyboard hints sit
inside a button as `<kbd>` (Deny `Esc`).

**Field** (`.field`). 36 px, bordered, accent border and a 3 px accent halo
on focus; `big` (48 px, 20 px mono) for the ID on Connect. Password fields
get an eye button. **Search** (`.search`) is a field with a leading icon.

**Dot matrix** (`svg.dm`). A grid of 9 px rounded squares on a 12 px
pitch. One series, one hue: empty squares `--dm-0`, filled squares the
accent steps by value; a column the app did not see is a dashed outline.
Each column has a `<title>` (the native tooltip: "10:42–10:44: 2
connected"), the whole chart an `aria-label` with the peak. Home's
activity chart: 30 columns of two minutes, 5 rows (5 or more fills the
column).

**Run bar** (`.runbar`). A row of 24 cells showing time left: amber, red
in the last 10 seconds. Used on approval cards.

**Table** (`.card.table`). A card without padding; a micro-label header row
on `--card-2`; rows of 56 px with hairlines, the first column an avatar
(initial on the accent tint, presence dot) with name and a sub-line, then
plain columns, then actions (Connect, ⋮). Rows highlight on hover.

**List row** (`.list-row`, `.sess`, `#trusted .item`). Avatar or tile,
name and a sub-line, chips, actions; hairline between rows.

**Approval card** (`.qcard`, and the request dialog). Monospace reference
(`REQ-014`), a chip (New device / Connected before / Asks to), the time
left; the question as the title; from where and the key; the run bar; what
to do. The one being asked about has an amber border and halo. The dialog
version adds the scam warning (red tint, icon) for new devices, a key/value
table, the access checkboxes (nothing ticked: view only), *Remember this
device*, and Deny (focused, `Esc`) then Allow.

```
┌──────────────────────────────────────────┐
│ REQ-014  (New device)               41 s │
│ DESKTOP-7QK2M wants to connect           │
│ 203.0.113.40 · 9b2e771c0aa4d3f1          │
│ ■■■■■■■■■■■■■■■■□□□□□□□□                 │
│ Answer in the dialog                     │
└──────────────────────────────────────────┘
```

**Dialog** (`dialog .dlg`). 460 px, 16 px radius, `--sh-3` on a scrim;
title 19 px; actions in a tinted footer bar, the safe action first.

**Settings group** (`.group` of `.item`). Card with rows of at least 56 px:
tile, label (and note), then the value, a control or a chevron. Dividers
start after the tile. Groups are headed by micro-labels; in wide windows an
index of the groups sits beside them.

**Empty state** (`.empty`). A 40 px dashed tile with a line icon (a solid
green tick for "nothing needs you"), one line in `--text`, one in
`--text-2` that says what will appear here and how.

**Help** (`.help`). The ⓘ button opens a tinted box with the explanation;
`aria-expanded` follows it.

**Update bar** (`.upd`). An accent-tinted rounded bar above the frame:
title, how to upgrade (or the command, with Copy), Upgrade now, Download,
What's new, skip (×).

**Snackbar** (`.snack`). Inverted pill at the bottom centre for 2.6 s.

**Banner window** (`overlay.html`). The small always-on-top window while
someone is connected: red tint, pulsing dot, who, Disconnect.

## Screens: personal edition

All screens exist in English and Chinese, light and dark. Element IDs that
the Go code, the smoke test and `testdata/page_test.mjs` rely on are listed
in [Working on the UI](#working-on-the-ui).

### First start (wizard)

A single centred card on the canvas; no sidebar.

```
            ┌────────────────────────────────────┐
            │ ▣                                  │
            │ FIRST START                        │
            │ Welcome to RemoHop                 │
            │ Connect to your computers, or ...  │
            │ ━━━━━━━━━━━━━━━  ───────────────   │
            │ Language                           │
            │ (Auto | 中文 | English)            │
            │                              [Next]│
            └────────────────────────────────────┘
   step 2: rendezvous server (official / my own / skip), "let others
   connect now", Back / Done
```

### Home (`#v-computer`)

This computer, who is connected, the computers you reach. The window opens
here.

```
FRIDAY, NOVEMBER 27 · 10:42
Home
This computer, who is connected to it, and the computers you reach.
┌ ▭ Allow incoming connections        ⓘ (●━)┐ ┌ ⚡ Quick connect ──────┐
│   ● Ready: every connection asks you      │ │ [123 456 789 ][Connect]│
│ ┌ ID ──────────────┐ ┌ TEMPORARY PASSWORD ┐│ │ RECENTLY SEEN          │
│ │ 104 713 644      │ │ 47a7-cwap          ││ │ [S] Studio iMac  Online│
│ │ Copy             │ │ Copy New Password… ││ │ [H] Home NAS     Online│
│ └──────────────────┘ └────────────────────┘│ │ [M] MUM-LAPTOP  3 h ago│
└────────────────────────────────────────────┘ │                        │
┌ ● Connected now ┐┌ ● Needs you ┐┌ ● Saved ──┐ │                        │
│ 2               ││ 0           ││ 6         │ │                        │
│ Control 1 ·…    ││ Nobody is…  ││ 3 online  │ ├────────────────────────┤
│      Sessions → ││ Approval q.→││ Address b.→│ │  All saved computers → │
└─────────────────┘└─────────────┘└───────────┘ └────────────────────────┘
┌ 🔑 Unattended access   ⓘ    (━●) ┐┌ ▥ Activity                 (Peak 2) ┐
│ ● On                              ││ Devices connected ... last hour     │
│ Running                           ││ ┄┄┄┄┄┄▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫▫      │
│ [Connections] [Change password]   ││ ┄┄┄┄┄┄▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪▪      │
└───────────────────────────────────┘│ 60 MIN AGO  RECORDED SINCE 10:01  NOW│
                                      └─────────────────────────────────────┘
```

- Off: the credentials give way to a dashed note ("Switch on to show this
  computer's ID and a password ...").
- 900-1180 px: the hero full width, the three KPIs in a row, then quick
  connect and unattended access side by side, then activity. Below 640 px
  one column.

### Connect (`#v-connect`)

The address book.

```
ADDRESS BOOK
Connect
Type an ID, a name you gave a computer, or host:port.
┌──────────────────────────────────────────────────────────────────┐
│ Computer ID or address                                           │
│ [ 123 456 789                                         ] [Connect]│
└──────────────────────────────────────────────────────────────────┘
Saved computers (6) ⓘ          (All|Online|Paired) [⌕ Search    ] [↻]
┌──────────────────────────────────────────────────────────────────┐
│ COMPUTER                     SYSTEM     LAST SEEN ONLINE          │
│ [S]• Studio iMac             macOS      Online        [Connect] ⋮│
│      418 203 775 · Online · Paired                               │
│ [M]• MUM-LAPTOP              Windows    3 hours ago   [Connect] ⋮│
│      731 550 204 · Offline · Password saved                      │
└──────────────────────────────────────────────────────────────────┘
```

⋮ opens the computer's menu (rename, wake, show or delete the saved
password, Wake-on-LAN address, forget). Empty: "No saved computers yet."
with what will appear. A filter or search with no match says so.

### Sessions (`#v-sessions`)

Who is waiting, who is connected, who may come back without asking.

```
SESSIONS AND APPROVALS
Sessions
┌ Needs you (2) ─────────────────┐ ┌ Connected now (2) ─────────────────────┐
│ ┌ REQ-014 (New device)  41 s ┐ │ │ [A]• Alex-Laptop                       │
│ │ DESKTOP-7QK2M wants to ... │ │ │      192.168.1.24 · 41:00 · Remembered │
│ │ ■■■■■■■■■■■■■■□□□□□□□□□□   │ │ │      (•View)(•Control)(Files) [Disc.]  │
│ │ Answer in the dialog       │ │ │ [S]• Sam's phone                       │
│ └────────────────────────────┘ │ │      (•View)(Control)(Files)   [Disc.] │
│ ┌ REQ-015 (Asks to)     52 s ┐ │ │              [⏻ Disconnect everyone]   │
│ │ Alex-Laptop asks for more  │ │ │ 🔑 Unattended access: [Connections]    │
│ └────────────────────────────┘ │ └────────────────────────────────────────┘
│ Refused after 60 s · Esc denies│ ┌ Remembered devices ⓘ ──────────────────┐
└────────────────────────────────┘ │ ▭ Alex-Laptop  a1f09c22…               │
┌ Computers open here ───────────┐ │   (•View)(•Control)(Files) Rename Forget│
│ [S]• Studio iMac               │ └────────────────────────────────────────┘
│      [Show] ⧉  Disconnect      │
└────────────────────────────────┘
```

The first request is also the dialog in front of every window, so the
queue is where the others wait their turn. Each card has an empty state.

### A computer open in the window

The sidebar lists it under *Open computers* (initial, connection dot:
amber connecting, green connected, grey closed; ⧉ pop out, × disconnect
and close). The frame shows its console (`internal/web` in embed mode),
which follows the app's theme.

### Settings (`#v-settings`)

```
PREFERENCES
Settings
Connection          CONNECTION ⓘ
General             ┌ 🌐 Rendezvous server          Official server  › ┐
Unattended access   └──────────────────────────────────────────────────┘
Updates             GENERAL
Support             ┌ 文A Language            (Auto | 中文 | English)   ┐
About               │ ☾  Theme                (Auto | Light | Dark)     │
                    │ ⏻  Start automatically                     (●━)  │
                    └──────────────────────────────────────────────────┘
                    UNATTENDED ACCESS / UPDATES / SUPPORT / ABOUT ...
```

Groups: Connection (rendezvous server, expands in place), General
(language, theme, start automatically), Unattended access (*Unattended ID
and password* opens its dialog), Updates (daily check, version and Check
now, the privacy note), Support (diagnostic report Export, help, Telegram),
About (privacy, terms, licenses, version and engine).

### Dialogs

Connection request (approval card, above), unattended access (password
twice, suggest one), password for incoming connections (temporary or my
own), unattended ID and password, the service's connections, small prompts
(rename, MAC address), the computer menu (a list of text buttons), a saved
password shown with Copy.

## Screens: team edition

The team edition (phase 2 of the [roadmap](../ROADMAP.md), commercial license) uses this design system and adds no colours of its own. Its screens are designed and built with the team edition, not in this repository.


## Accessibility

- Contrast: all text pairs at least 4.5:1 in both themes (see Colour).
  Status is never colour alone: dots have words, chips have labels, the
  red/green switch has the state written beside it.
- Focus: every control is a button, link, input or `tabindex="0"` element
  with a 2 px accent outline, 2 px offset, on `:focus-visible`. The request
  dialog focuses Deny; `Esc` denies.
- The sidebar is a `nav` with `aria-current="page"` on the current place;
  the icon rail keeps names as `title` and `aria-label`; each page is a
  region labelled by its title.
- Charts have an `aria-label` summary and per-column `<title>`s; Windows
  high-contrast mode (`forced-colors`) keeps marks and the selection
  visible.
- `prefers-reduced-motion` stops the pulse and the switch transition.
- Text set by other machines (device names, titles) is only ever placed
  with `textContent`.

## Working on the UI

- Files: `index.html` (markup and icon sprite), `style.css` (tokens and
  components), `app.js` (state, strings in `T.en` and `T.zh`),
  `overlay.html` and `overlay.js` (the banner window).
- Every visible string has a key in both `T.en` and `T.zh`: `data-t` sets
  the text, `data-ta` the `aria-label` and `title`, `data-tp` the
  placeholder.
- Icons are 24 px line symbols in the sprite (`#i-...`), drawn with
  `svg.i` (1.75 px round stroke in `currentColor`).
- No inline styles (the CSP forbids `style` attributes); SVG geometry goes
  in attributes, colour in classes.
- IDs that other code relies on: the views `v-wizard`, `v-computer`,
  `v-connect`, `v-sessions`, `v-settings` (the smoke test shows `computer`,
  `sessions`, `connect`, `settings` and `open:ID`); `bar` (hidden during the wizard),
  `tabs` with `tab-computer`, `tab-connect`, `tab-settings` and the
  `.tab.host` rows (`.dot`, `.name`, `.x`); `badge`; `incoming`, `creds`,
  `in-id`, `in-pw`, `in-pw-l`, `regen`, `pw-reveal`, `pw-set`; `sessions`,
  `sessions-box` with `.sess .chip` (View, Control, Files) and
  `.btn.danger`; `peers` with `.peer .s` and `.peer .btn` (Connect);
  `peers-empty`; the dialogs and their fields (`dlg-req`, `req-*`,
  `dlg-pw`, `pw-*`, `dlg-un`, `un-*`, `dlg-uncreds`, `dlg-svc`,
  `dlg-ask`); `set-theme`, `set-lang`, `srv-row`, `srv-panel`, `srv-val`;
  `wiz-*`.
- Check a change with `REMOHOP_BROWSER_TEST=1 REMOHOP_SHOTS=/tmp/shots go
  test -run TestPageInBrowser ./internal/desktop/` (needs node and Chrome):
  it drives the real page against a real app and keeps screenshots.
