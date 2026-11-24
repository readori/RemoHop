# Snap package (maintainers)

`snapcraft.yaml` here builds the snap `remohop` for amd64 and arm64. Users: see
[docs/en/install.md](../../docs/en/install.md). Status: the name is registered and private, pending the
store's name review, and the classic-confinement request (below) has not been filed yet. Until both are
done nobody can install it from the store, and the site's `CHANNELS` in `site/build.py` stays without a
Snap entry.

## What is in it

| Path in the snap | What |
|---|---|
| `bin/remohop` | the `remohop` binary from the release's `remohop_linux_<arch>.tar.gz`, byte for byte |
| `usr/share/doc/remohop/{LICENSE,NOTICE,THIRD_PARTY_LICENSES.txt}` | from the same archive |
| `meta/gui/quick.desktop`, `meta/gui/icon.svg` | launcher entry "RemoHop Quick Support" (`gui/remohop-quick.desktop`, `gui/icon.svg`) |

Apps: `remohop` (the CLI, `/snap/bin/remohop`) and `quick` (`remohop.quick`, which runs `remohop quick`, the
quick-support window; it is what the launcher entry starts). `remohop-quick` in the release tarballs is the
same program built for a double-click, so the snap does not carry a second 18 MB copy. The binary is static
(`CGO_ENABLED=0`), so nothing from `core24` is used at run time.

**Nothing is compiled.** The pull step downloads `SHA256SUMS` of the release, checks it against the hash
pinned in `snapcraft.yaml`, then checks the archive against `SHA256SUMS`, so the snap's program is exactly
the one users can verify with cosign. The hash comes from `bump.sh`, which checks the cosign signature of
`SHA256SUMS` before pinning it. The version is the tag without its `v` (`craftctl set version`).

## Why classic confinement

`remohop` runs the host side of a remote-control session as the user who started it (or as a systemd
service). Strict confinement would break most of what it exists for:

- **Remote shells and jobs** must run the user's real login shell, with their environment, PATH and tools
  (`git`, `docker`, editors, compilers). A strict snap runs in its own mount namespace on top of `core24`:
  the host's `/usr` is not where the shell expects it, and AppArmor would stop it from running arbitrary
  programs.
- **Files**: copy, sync, mount and "deliver to Downloads" act on any path the user can reach, including
  dot-directories, `/etc`, `/srv`, other disks. `home` and `removable-media` cover only part of that, and
  `system-files` takes a fixed list of paths.
- **Port forwarding to anything, VPN (TUN), USB/IP (`vhci-hcd`, sysfs), serial ports, FUSE mounts,
  Wake-on-LAN, X11 and Wayland screen capture and input injection (`uinput`/XTEST)**: some have
  interfaces, several only on Ubuntu Core or with a gadget's slot, and together they amount to the whole
  machine anyway.
- **`remohop service install`** writes and enables systemd units (user and system) that run `remohop`.

Classic snaps behave like a `.deb`: no sandbox, the host's filesystem. It needs a manual review by the
Snap Store, requested on the forum (post below).

### Snap-specific behaviour (needs a release with `internal/snapenv`; v1.0.0 lacks it)

- `snap run` adds `SNAP`, `SNAP_NAME`, `SNAP_REVISION` and other `SNAP_*` variables to the environment. The
  program removes them at start (`internal/snapenv`), so remote shells and the programs the host starts get
  the user's own environment. Classic snaps keep `HOME` and `XDG_RUNTIME_DIR` as they are.
- `remohop service install` from the snap writes `ExecStart=/snap/remohop/current/bin/remohop host`, not
  the revision directory (`/snap/remohop/x1/...`), which snapd deletes a few refreshes later. A running
  service keeps running the old revision after a refresh until it restarts: `remohop service restart`
  (or a reboot). With v1.0.0 pass the path yourself:
  `remohop service install --exe /snap/remohop/current/bin/remohop`.
- The snap installs no systemd unit of its own and enables nothing; `remohop service install` is the way to
  run the host at boot or login, as with the other packages.

Because of the first two points, release v1.0.0 should go to `edge` only; promote the first release that
contains `internal/snapenv` to `stable`.

## Build

On Ubuntu 24.04 (the base is `core24`), in this directory:

```bash
sudo snap install snapcraft --classic
sudo snapcraft pack --destructive-mode        # builds for this machine's architecture
# or, on any distribution with LXD: snapcraft pack
sudo snap install --dangerous --classic ./remohop_*_amd64.snap
remohop version && remohop.quick --no-open
```

CI: the workflow `remohop-snap.yml` in the CI repository (`readori/test-env-setup`) builds both
architectures on native runners, checks the snap (metadata, desktop file, that `bin/remohop` is the
release binary), installs it, runs `test/smoke/smoke.sh /snap/bin/remohop` and checks `remohop.quick`,
the launcher entry and the snap-specific behaviour above (with a binary built from the ref, including a real
`service install --system` across a refresh). Request a run with `ci-requests/remohop-snap.env`:

```
REF=main
VERSION=v1.2.3      # optional: default is the version snapcraft.yaml pins
MODE=build          # or upload
```

`MODE=upload` additionally runs `snapcraft upload --release=edge` for both snaps; it needs the secret below.

## Releasing a new version

1. `packaging/snap/bump.sh vX.Y.Z --verify require` (needs cosign: `scripts/install-cosign.sh` installs the
   pinned one). It rewrites `VERSION` and `SUMS_SHA256` in `snapcraft.yaml`; commit that.
2. Run the CI workflow with `MODE=upload`: both snaps go to `edge`.
3. Test from the store: `sudo snap install remohop --classic --edge`.
4. Promote: `snapcraft release remohop <revision> candidate,stable` for each architecture's revision
   (`snapcraft status remohop` lists them), or in the web dashboard.

The CI run can package any release without the commit (it runs `bump.sh` for `VERSION` itself), but keep
the file in step with what is in the store.

## One-time setup (the owner of the snap name)

1. **Name review:** the name `remohop` is registered and private, waiting for the store's review. Nothing to
   do but wait (or answer the reviewer).
2. **Classic confinement:** post the request below in the forum category
   [store-requests](https://forum.snapcraft.io/c/store-requests). The reviewers usually answer in the
   thread within days and may ask questions. Upload is possible before approval, but the store holds a
   classic snap for manual review until the request is granted.
3. **CI credentials:** on a machine with snapcraft, logged in to the account that owns the name:

   ```bash
   snapcraft export-login --snaps=remohop \
     --acls=package_access,package_push,package_update,package_release \
     --expires=2027-12-31 remohop-ci.cred
   ```

   Add the file's content as the repository secret `SNAPCRAFT_STORE_CREDENTIALS` of `readori/test-env-setup`
   (Settings, Secrets and variables, Actions), then delete the file. It can only upload and release this
   snap; renew it before it expires.
4. After the store has approved both and the listing is public: fill in the store page (screenshots,
   categories "Utilities" and "Development"), add a Snap row to `CHANNELS` in `site/build.py`, and drop the
   "coming soon" from the Snap rows in `docs/{en,zh}/install.md`.

### Classic confinement request (forum post draft)

Title: **Classic confinement request: remohop**

> Hello, I'd like to request classic confinement for the snap **remohop**.
>
> - Project: RemoHop, open source (AGPL-3.0), https://github.com/readori/RemoHop
> - snapcraft.yaml: https://github.com/readori/RemoHop/blob/main/packaging/snap/snapcraft.yaml
> - What it is: a peer-to-peer remote-control tool, comparable to an SSH server and client plus remote
>   desktop in one static Go binary. On the machine to be controlled, `remohop host` (often installed as a
>   systemd service with `remohop service install`) accepts end-to-end encrypted, authenticated sessions
>   from the owner's other devices. Within a session the owner gets interactive shells and background
>   jobs, file copy/sync and FUSE mounts, TCP/UDP port forwarding, a VPN link (TUN), USB/IP and serial
>   device sharing, clipboard, screenshots and remote desktop (X11/Wayland capture and input injection).
>   The snap also has `remohop.quick`, a quick-support window (shows an ID and a one-time password and
>   asks the local user to approve every connection).
>
> Why classic: the host is a remote shell and machine-administration tool, so it must act as the user on
> the real system, the way sshd or a terminal emulator does:
>
> 1. Remote shells must run the user's own login shell with their real environment and host tools (git,
>    compilers, docker, editors). Under strict confinement the shell would run inside the snap's mount
>    namespace on top of core24 and under its AppArmor profile, so the user's tools would be missing or
>    denied.
> 2. File transfer, sync and mounts act on any path the user can access (dot-directories, /etc, /srv,
>    other disks); `home`, `removable-media` and `system-files` cannot express "whatever the user can
>    reach".
> 3. It installs and enables systemd units (user and system scope) that run the host at boot or login.
> 4. The rest needs a long list of super-privileged interfaces (network-control for TUN, raw-usb and
>    kernel-module-load for USB/IP via vhci-hcd, serial-port, fuse-support, uinput, x11, wayland screen
>    capture, network-observe), some of which are only usable on Ubuntu Core or with a gadget slot. Even
>    with all of them, point 1 and 3 remain impossible.
>
> We considered strict confinement with those interfaces and a devmode build; neither gives a usable remote
> shell. Category: we believe this fits the same reasoning as terminal emulators and remote
> administration/orchestration agents: the software's purpose is to run arbitrary commands chosen by the
> machine's owner.
>
> The snap repackages the binaries of our signed GitHub releases (checked against the release's
> cosign-signed SHA256SUMS during the build); it starts no daemon on install, and every remote access
> requires the user's password, a pairing or an authorised key (quick support: an explicit local approval).
>
> Thank you!
