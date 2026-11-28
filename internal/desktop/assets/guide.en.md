<!--
The user guide shown on the app's Guide page (app.js renders it). One file
per language, guide.en.md and guide.zh.md, with the same section ids in the
same order: the ⓘ buttons open sections by id. What is understood:
"## Title {#id}" starts a section (listed in the contents), "### Title {#id}"
a part of one (the id is optional), paragraphs, "- " and "1. " lists,
"> " a highlighted note, **bold**, `code`, [text](#id) to another place in
the guide, [text](app:connect) to a page of the app (computer, connect,
sessions, settings) and [text](web:docs/ai-agents) to a page of
remohop.com that update.go's sitePage allows. Nothing else, and no HTML.
Use the app's own labels, in bold.
-->
# User guide

## Getting started {#start}

RemoHop connects you to your other computers, and lets someone you trust connect to this one. Every connection is end-to-end encrypted, and nobody gets in without the password and your answer.

### Windows {#start-windows}

1. Unzip the download and double-click **RemoHop.exe**.
2. RemoHop is not code-signed yet, so Windows SmartScreen may warn the first time. Click **More info**, then **Run anyway**.
3. Windows Firewall may ask about network access. Either answer works on most networks.

### macOS {#start-mac}

1. Open the downloaded `.dmg` and drag **RemoHop** to Applications, then open it from there.
2. Before someone can see this Mac's screen, allow RemoHop in **System Settings › Privacy & Security**: **Screen Recording** to show the screen, **Accessibility** for the mouse and keyboard. Screen Recording takes effect after RemoHop is started again.
3. **RemoHop Quick Support** is a separate app for macOS, so it needs the same two permissions of its own.

### Linux {#start-linux}

Unpack the download and run `./remohop-app`. Its window uses WebKitGTK; without it, RemoHop opens in your browser instead, and works the same.

### First start {#start-first}

RemoHop asks for the language and the **rendezvous server**, which introduces computers to each other by their ID:

- **Official server**: run by RemoHop, nothing to set up. Choose this if unsure.
- **My own server**: the address of a server you or your company run.
- **No server**: connect only by address, on the same network.

Tick **Let others connect to this computer now** if someone is about to help you. Both can be changed later in [Settings](#settings).

The window closes to the tray (the menu bar on macOS), so others can still connect. **Quit** in the tray menu ends RemoHop and every connection.

## Connecting to a computer {#connect}

Go to [Connect](app:connect), or use **Quick connect** on Home.

### By ID {#connect-id}

Type the other computer's 9-digit ID (it is on its Home page) and click **Connect**. Both computers must use the same rendezvous server.

### By address {#connect-address}

On the same network, or without a server, type the address and port, like `192.168.1.5:21118`.

### On this network {#lan}

**On this network** lists the computers near you that run RemoHop with incoming connections on. No server and no typing: click **Connect** beside one. The list is refreshed every few seconds while Connect is shown.

A computer marked **Key changed** claims to be one you saved, with another key. It may be an impostor: do not connect until you have checked with its owner (see [Key changed](#trouble-key)).

### The password, and remembering a computer {#connect-password}

The computer opens in the window and asks for its password: the one its owner reads you, or the one shown on its Home page.

Tick **Remember this computer** to connect without a password next time. RemoHop pairs with it; where that is not possible (Quick Support, another RemoHop app) it saves the password on this computer instead, until that password changes.

The RemoHop app and Quick Support then ask the person at that computer, who sees who you are and presses **Allow**. If nobody answers within about a minute, the connection is refused.

### Saved computers {#saved}

Every computer you connected to or paired with is listed under **Saved computers**, with whether it is online. **Paired** ones need no password. Its menu (**⋮**) can **Rename** it, show or delete a saved password, **Forget** it, and wake it from sleep on the same network (**Wake up**, once you gave it a **Wake-on-LAN address**).

### An open computer {#connect-open}

Each open computer is listed in the sidebar under **Open computers**. **⧉** moves it into a window of its own; **×** disconnects and closes it. A connection that drops (sleep, a new network) comes back by itself.

## Letting others connect {#incoming}

### Allow incoming connections {#incoming-allow}

Switch on **Allow incoming connections** on [Home](app:computer). Home then shows this computer's **ID** and a **Temporary password**, each with **Copy**. Read them to the person who helps you, and to nobody else.

The temporary password is new each time RemoHop starts; **New** replaces it at once. **Password settings** chooses **My own password** instead, which stays until you change it (at least 8 characters, with letters and digits). Either way, every connection still asks you first.

### Approving a connection {#approval}

When someone asks to connect, a **Connection request** comes to the front, over every window. It shows the name the other side gave itself, its key and where it connects from.

> A **New device** comes with a warning. If someone called you saying they are from support, your bank or the police and asked you to install this, press **Deny**. That is a scam.

The other side **only sees your screen** unless you tick **Control the mouse and keyboard** or **Transfer files and use the clipboard**. **Deny** is chosen when you press `Esc`; a request nobody answers is refused after a minute.

**Remember this device** lets that device in next time with the same access, without asking, while incoming connections are on.

### Who is connected {#sessions}

[Sessions](app:sessions) shows the requests waiting for you, and everyone **Connected now**. **Control** and **Files** switch that access on or off at once. A device that only watches can ask for more from its side; you are asked again.

**Remembered devices** come in without asking. Change their access with the same switches, **Rename** them, or **Forget** them.

### Ending a connection {#ending}

- **Disconnect** beside a device ends its connection.
- **Disconnect everyone** ends all of them.
- While anyone is connected, a small red banner stays on top of all windows in the corner of the screen, with **Disconnect**.
- Switching **Allow incoming connections** off ends every connection and lets nobody new in.

### Unattended access {#unattended}

To reach this computer when nobody is at it, switch on **Unattended access** on Home and choose a strong password (at least 12 characters, with three of: lower case, upper case, digits, symbols). **Suggest a strong password** makes one. Anyone with the unattended ID and this password connects **without asking**, so keep it safe.

It runs as a background service from start-up. The switch is green while it is on and red while it is off. **Connections** shows who is connected through it, and **Change password** replaces the password. Its ID and password are under **Settings › Unattended ID and password**; on Windows the service has an ID of its own.

On Windows each of these steps asks for an administrator, because the service belongs to the administrators.

## Remote desktop {#desktop}

An open computer has the tabs **Desktop**, **Terminal**, **Files**, **Clipboard** and **Info**.

### The Desktop toolbar {#desktop-toolbar}

- **View only** stops sending your mouse and keyboard. When the other side allowed only viewing, **Request control** asks its owner.
- **Fit** scales the picture to the window; **Fullscreen** fills the screen.
- **Refresh** asks for a fresh picture.
- **Ctrl+Alt+Del** sends the key combination your own computer keeps for itself.
- The quality menu: **Native size**, or at most 2160p down to 480p. Smaller is faster on a slow link.
- The frame-rate menu: **Auto frame rate**, or 30 to 144 fps.
- The monitor menu appears when the other computer has several screens.
- **Stats** shows the resolution, codec, bitrate and latency.
- **Reconnect** appears when the picture has stopped.

Click the picture first: the keyboard goes to the other computer while the picture has the focus.

### The full viewer {#desktop-viewer}

`remohop <ID> desktop` in a terminal opens a fuller viewer in your browser. It draws the remote pointer on this computer, so it follows your hand without delay, and adds:

- **Lock mouse** (or `Ctrl+Alt+Shift+M`) for games and 3D apps: the mouse stays in the picture and sends its movement. `Esc` or the shortcut releases it. With **Auto lock**, the next click on the picture locks it when a remote app hides its pointer.
- **Audio** plays the other computer's sound here; **Mute** silences it.
- **Record** saves what you see to an MP4 file on this computer.
- Key combinations, typing text, quality presets, and locking, blocking or blacking out the remote screen.

### Screen sharing component (ffmpeg) {#ffmpeg}

The computer whose screen is shared needs ffmpeg. Without it, Home says so while incoming connections are on; files and commands still work.

On Windows and macOS, **Install screen sharing component** downloads a build of ffmpeg named in RemoHop, checks it against a fingerprint built into RemoHop before using it, and shows the progress. Nothing is downloaded until you click. On Linux, install it with your package manager, for example `sudo apt install ffmpeg`.

## Files, terminal and clipboard {#files}

- **Files** browses the other computer: **Up**, **New folder**, **Upload…** (or drop files on the list), and **Download**, **Rename** or **Delete** on each file. An existing file is never replaced without asking: **Skip**, **Keep both** or **Replace**.
- **Terminal** opens a command line there. **New session** starts another one; a session keeps running when the connection drops, and you can pick it again from the session menu. **Copy** copies the selected text, **Paste** pastes.
- **Clipboard**: **Fetch remote clipboard** brings its text here (**Copy to this device**); **Send to remote** puts text there.
- **Info** shows the system, and **Take screenshot** takes a picture of its screen.

Each needs the other side's permission: files and the clipboard come with **Transfer files and use the clipboard**.

## Quick Support {#quick}

To help someone just once, they use **RemoHop Quick Support** instead: nothing stays installed, and closing its window ends everything. Tell the person you help:

1. Download **RemoHop Quick Support** from remohop.com and open it (on Windows: **More info**, **Run anyway**; on a Mac, allow Screen Recording and Accessibility).
2. Keep **Use the official server**, unless you gave them the address of your own.
3. Read me the **ID** and the **Password**, and nobody else.
4. When my request appears, check the name and press **Allow**.
5. When we are done, press **Stop sharing** or close the window.

You then connect from [Connect](app:connect) with that ID and password. Their window decides what you may do besides seeing the screen: mouse and keyboard, files and clipboard (on), commands and network (off).

## AI agents {#agents}

An AI agent can work on a computer through RemoHop with less than full access. On that computer, `remohop host grant NAME --for 2h --allow ...` gives it a credential that allows only what you list, for a limited time; `remohop host revoke NAME` ends it, and `remohop host panic` ends every agent session at once. The agent connects with `remohop mcp`. See [AI agents on remohop.com](web:docs/ai-agents).

## Settings and updates {#settings}

- **Rendezvous server**: official, your own, or none ([First start](#start-first)).
- **Language** and **Theme**: Auto follows the system.
- **Start automatically**: RemoHop starts in the tray when you sign in.
- **Updates**: once a day RemoHop reads the newest version number from remohop.com. Nothing about this computer is sent, and nothing is installed without you. When there is a newer version, a bar above the page offers **Upgrade now** or **Download**, and **What's new**.
- **Diagnostic logs**: **Export** saves a text file to your Downloads folder, to send to support when something does not work. Passwords, tokens and keys are removed. It goes nowhere unless you send it.

## Troubleshooting {#trouble}

### No rendezvous server is set {#trouble-noserver}

An ID is looked up on a rendezvous server. Choose one in [Settings](app:settings) › **Rendezvous server**, or connect by the computer's address (`host:port`).

### The computer is offline, or the ID is unknown {#trouble-offline}

Check the ID with the person at that computer: RemoHop must be running there with **Allow incoming connections** on, and both computers must use the same rendezvous server. A computer that sleeps is offline; **Wake up** in its menu (**⋮**) can wake it on the same network.

### Can't reach the server {#trouble-server}

Home says **Can't reach** the server, or **Offline: trying again…**. Check this computer's internet connection, and the address in [Settings](app:settings) › **Rendezvous server** (the **Rendezvous server settings** link on Home leads there). On the same network you can also connect by address.

### Screen sharing needs ffmpeg {#trouble-ffmpeg}

The computer whose screen is shared has no ffmpeg. Install it there with **Install screen sharing component** on its Home page (Windows and macOS), or with its package manager on Linux. See [Screen sharing component](#ffmpeg).

### Key changed {#trouble-key}

The computer answered with another key than the one saved here. If its owner reinstalled RemoHop or the computer, **Forget** it in **Saved computers** (**⋮**) and connect again with its password. If not, do not connect: someone may be pretending to be it.

### Wrong password, or denied {#trouble-password}

A temporary password changes each time RemoHop starts there: ask for the current one. "Denied" means the person there pressed **Deny**, or nobody answered within a minute.

### Still stuck {#trouble-more}

**Settings › Diagnostic logs › Export** saves a report; send it with **Help and support** or on Telegram.
