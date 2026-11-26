# Packaging and releases (maintainers)

How RemoHop is built, signed, published and handed to package managers. Users: see
[docs/en/install.md](../docs/en/install.md).

## What a release contains

For a tag `vX.Y.Z` the release has these assets (names carry no version, so
`releases/latest/download/NAME` is a stable URL):

| Asset | Made by |
|---|---|
| `remohop_linux_{amd64,arm64}.tar.gz` | `scripts/build-release.sh package` |
| `remohop_darwin_universal.tar.gz` (arm64 + x86_64, Developer ID signed, notarized) | built and signed in the CI repo, wrapped by `build-release.sh darwin` |
| `remohop_windows_{amd64,arm64}.zip` (unsigned for now) | `build-release.sh package` |
| `RemoHop-windows-{amd64,arm64}.zip`: the desktop app, `RemoHop.exe` (unsigned for now) | `build-release.sh build` (via `packaging/quick/build.sh`) + `package` |
| `RemoHop-QuickSupport-windows-{amd64,arm64}.zip`: quick support, the same `RemoHop.exe` renamed `RemoHop-QuickSupport.exe` (the name makes it start in quick mode, see `cmd/remohop-quick/launch.go`; a rename keeps a signature) | `build-release.sh package` |
| `RemoHop-macos.dmg`: the desktop app, the usual Mac download: disk image with `RemoHop.app` and an Applications link; the image and the app are Developer ID signed, notarized and stapled |
| `RemoHop-macos.zip`: the same `RemoHop.app` as a `ditto` zip (signed, notarized, stapled), for scripts and tools that want a zip | built, signed, notarized and stapled in the CI repo, checked and copied by `build-release.sh darwin-app` |
| `RemoHop-QuickSupport-macos.zip`: quick support, `RemoHop Quick Support.app` (the same program as `Contents/MacOS/RemoHop-QuickSupport`, bundle id `com.remohop.quicksupport`) as a `ditto` zip, signed, notarized, stapled: unzip and open, nothing to install | likewise |
| `remohop-app_linux_{amd64,arm64}.tar.gz`: the desktop app for Linux (`remohop-app`, `remohop.desktop`, icon; needs glibc, and WebKitGTK for its own window) | `build-release.sh build` + `package` |
| `remohop-quick_linux_{amd64,arm64}.tar.gz`: quick support for Linux (the same program as `remohop-quick`) | `build-release.sh build` + `package` |
| `remohop_<ver>_{amd64,arm64}.deb`, `remohop-<ver>-1.{x86_64,aarch64}.rpm`, `remohop_<ver>_{x86_64,aarch64}.apk` | nfpm, from `packaging/nfpm.yaml` (prerelease names get `.` for nfpm's `~`, which GitHub would rename) |
| `remohop_sbom.cdx.json` | `scripts/sbom.sh` (CycloneDX, from `go version -m` of the linux, windows and darwin builds) |
| `install.sh`, `install.ps1` | copied from `packaging/` by `scripts/finalize-release.sh` |
| `SHA256SUMS` | `finalize-release.sh`, over every file above (it refuses to run when one of the version-free archives is missing) |
| `SHA256SUMS.sigstore.json` | cosign keyless signature bundle over `SHA256SUMS` |

Also published, not as release assets: the image `ghcr.io/<owner>/remohop-signal:<tag>` (multi-arch,
signed, SBOM + provenance attached), and a workflow artifact `package-manager-manifests` (stable tags only).

Every archive contains the binary, `LICENSE` (AGPL-3.0), `NOTICE` and `THIRD_PARTY_LICENSES.txt`
(`packaging/third_party_licenses.sh`: the Go project's license and every linked module's); `RemoHop.app` carries
the same three files in `Contents/Resources`, inside the signature, and the container image has them at `/`. Archives and the SBOM are reproducible: the same commit
and Go toolchain give byte-identical files (`SOURCE_DATE_EPOCH` is the commit time).

## Cutting a release

1. Merge to `main`; make sure CI is green and `docs/` match.
2. Optional dry run: Actions, "Release", Run workflow, version `v0.0.0-test.1`. It builds and keeps the
   files as the artifact `assets-linux-windows`; it never signs, releases or pushes.
3. Create the tag `v1.2.3` on the release commit, which must be on `main` (`verify` refuses any other). Tags
   are created through the GitHub API by the owner's operations session, e.g.
   `gh api repos/readori/RemoHop/git/refs -f ref=refs/tags/v1.2.3 -f sha=<full commit SHA>`. Tags with a `-`
   (`v1.2.3-rc.1`) become prereleases and are not tagged `latest` in Docker or rendered into manifests.
4. The workflow runs `verify` (tag format first, then: on `main`, gofmt, vet for linux/darwin/windows, the unit
   tests of both modules, govulncheck); nothing else starts unless it passes. Then `build`, and `macos` (starts the
   signing workflow in the CI repo and waits, about 10 to 20 minutes, then checks the returned files against a
   rebuild, see below), then `publish` (draft release, signature, publish), then `docker`.
5. Verify (below), then submit the package-manager files (below) and the Store package
   ([windows/msix/README.md](windows/msix/README.md)).
6. Website: set `RELEASED = True` in `site/build.py`, run `python3 site/build.py --release-docs` (rewrites the
   "no release yet" statements in `README.md`, `README.zh-CN.md` and `docs/`, listed in `RELEASE_DOC_EDITS`), then
   `python3 site/build.py`, and commit. As each package manager goes live, set its `"live": True` in `CHANNELS`
   there; when the phone apps are in the stores, set `STORES_LIVE = True` and run `--release-docs` again.

If `verify`, `build` or `macos` fails, nothing is released; re-run the failed jobs (a failed `verify` on a
broken commit means a new tag on a fixed commit). If `publish` fails after creating the draft, re-run it: it
continues with that draft and replaces its files. If only `docker` fails, the release is out; re-run that job.

### Secrets and variables

| Name | Where | Needed for |
|---|---|---|
| `CI_REPO_TOKEN` (secret) | RemoHop repo | the `macos` job: a fine-grained personal access token, resource owner `readori`, **only** repository `test-env-setup`, permission "Actions: Read and write". Lets it start and read runs of the signing workflow and download its artifact. Nothing else. Set an expiry and rotate it. The job also checks that the run it follows was started by this token's account. |
| `APPLE_TEAM_ID` (in `release.yml`, not a setting) | RemoHop repo | the Apple team whose Developer ID must sign the macOS files (`H7FLAAL9RB`). Public; changing it is a reviewed change to the workflow. |
| `MACOS_DEVID_P12_BASE64`, `MACOS_DEVID_P12_PASSWORD`, `ASC_KEY_ID`, `ASC_ISSUER_ID`, `ASC_PRIVATE_KEY`, `ASC_TEAM_ID`, `TARGET_REPO_PAT` (secrets) | **CI repo** | signing, notarization, and checking out the RemoHop source (`TARGET_REPO_PAT` is needed only while the RemoHop repository is private). They never need to exist in the RemoHop repo. |
| `WINDOWS_SIGNING` (variable) | RemoHop repo | unset today. Switches the Windows hook on (see Windows signing). |
| `SKIP_MACOS` (variable) | RemoHop repo | `true` releases without macOS. For tests only: `install.sh` and Homebrew need the macOS asset. |

The workflow's own `GITHUB_TOKEN` creates the release and pushes the image (job-level `permissions`,
`contents: write`, `packages: write`, `id-token: write`); there is no other long-lived secret. No signing key
is stored anywhere.

## Where the CI pieces live, and why

- **`.github/workflows/release.yml` (RemoHop repo)** does everything that needs no secret from Apple: builds,
  packages, checksums, signing, release, image. It belongs next to the code it releases, runs on the tag, and
  its OIDC identity is what users verify.
- **`sodesk-macos-release.yml` (CI repo `readori/test-env-setup`; the CI workflow files keep their pre-rename `sodesk-` file names)** keeps the Developer ID certificate and
  App Store Connect key, which live there with a hardened workflow (no pull_request triggers, SHA-pinned
  actions, temporary keychain, strict request parsing). The release workflow calls it with `workflow_dispatch`
  (`ref=<commit>`, `version=<tag>`, `upload=true`, `correlation=<id>`) and follows the run whose ID the
  dispatch API returns (falling back to the correlation string), after checking that it is that workflow on the CI
  repository's `main`, dispatched by the token's account, for this commit. With `upload=true` it also uploads the
  signed binary, `RemoHop-macos.zip`, `RemoHop-macos.dmg` and `RemoHop-QuickSupport-macos.zip` as the artifact `remohop-macos-universal` (3 days).
  The release job does not take those files on the CI repository's word:
  `scripts/verify-macos-release.sh` rebuilds the darwin binaries and `RemoHop.app` from the tagged commit (Go builds
  are reproducible: same source, Go and flags give the same bytes on any host) and `scripts/machocmp` requires each
  signed slice to be byte for byte the rebuild before the code signature, with nothing added but that signature,
  whose code directories must cover exactly that code with matching page hashes, carry no entitlements, have the
  hardened runtime, the identifier `com.remohop.cli` (`com.remohop.quick` for the app) and the team
  `APPLE_TEAM_ID`. The app must hold exactly the rebuilt files plus its signature and stapled ticket, and the disk
  image (read with 7-Zip) exactly that app and the `/Applications` link. The certificate chain and the
  notarization are checked by codesign, spctl and stapler in the signing run and by Gatekeeper on every Mac. Then
  the binary is wrapped into `remohop_darwin_universal.tar.gz`; `build-release.sh darwin-app` checks the app's
  layout once more and copies the zip unchanged. A compromise of the CI repository can therefore at worst ship this
  commit's code with a broken signature (which macOS refuses), not other code. The CI repository should still
  protect `main` and its workflow file (branch protection, required review). The same run signs
  `RemoHop.app` as a whole (hardened runtime, timestamp, no entitlements, identifier `com.remohop.quick` from
  `Info.plist`), notarizes it, staples it, and checks a quarantined copy unpacked from the zip with `spctl`, its UI
  server, and a LaunchServices start. Requests that do not set `upload` upload nothing.
- *Alternative:* turn the CI workflow into a reusable workflow (`on: workflow_call`) and call it from
  `release.yml` with `uses: readori/test-env-setup/.github/workflows/sodesk-macos-release.yml@<sha>`. That removes
  the polling, but the Apple secrets would then have to be present in (or inherited by) the RemoHop repo, which
  is exactly what the dispatch design avoids. Revisit if the polling ever becomes a problem.

## Checksums, signature and provenance

- **cosign keyless** signs `SHA256SUMS`, because it needs no long-lived secret: the job gets a short-lived
  GitHub OIDC token, Sigstore's Fulcio issues a certificate naming this repository, workflow file and tag, and the
  signature is recorded in the public Rekor log. There is no private key to leak, rotate or lose; what users trust
  is "this GitHub workflow, for this tag", which is what `install.sh`, `install.ps1` and `snap/bump.sh` check
  (`--certificate-identity` `.github/workflows/release.yml@refs/tags/<the requested tag>` and the GitHub issuer;
  "latest" is resolved to its tag first, so an older signed release cannot be passed off as the requested one). The
  trade-offs: verifying needs cosign (the scripts continue on the checksum alone when it is missing, unless
  `--verify require`), and the identity includes the repository path, so a rename or transfer changes it (update
  `install.sh`/`install.ps1`/`snap/bump.sh`). The identity is only as strong as control over `v*` tags: restrict
  creating, updating and deleting them to the owner with a tag ruleset.
  minisign would work offline but needs a secret key in CI and a public key distributed out of band.
- Because the whole release hangs on `SHA256SUMS`, one signature covers every file and the installers.
- **cosign** itself is downloaded by `scripts/install-cosign.sh` as a pinned version whose SHA-256 is checked
  (hashes copied from the project's `cosign_checksums.txt`). To upgrade, change the version and both hashes together.
- **SBOM:** `remohop_sbom.cdx.json` lists the Go toolchain and every module linked into the release binaries
  (union over the linux, windows and darwin builds; the darwin builds are the code of the signed macOS binary, which
  the `macos` job checks, and macOS links `github.com/ebitengine/purego`, which the others do not). It is generated
  from the module data Go embeds in each binary, so no extra tool is downloaded. The Docker image additionally carries
  BuildKit's SBOM and SLSA provenance (`--sbom=true --provenance=mode=max`) as OCI attestations.
- **Provenance:** the Fulcio certificate in the bundle records repository, workflow, tag and commit SHA; the
  macOS binary additionally comes with Apple's notarization record. When the repository is public you can add
  GitHub build attestations (`actions/attest-build-provenance`, pinned by commit SHA) to the `publish` job
  (`subject-path: assets/*`, `permissions: attestations: write`); it is left out because it needs a public repository
  (or a paid plan) and the SHA of the action to pin.

### Verifying a release

```bash
gh release download v1.2.3 -R readori/RemoHop -p 'SHA256SUMS*' -p 'remohop_linux_amd64.tar.gz'
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/readori/RemoHop/.github/workflows/release.yml@refs/tags/v1.2.3 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
```

## In-app upgrades (Ed25519)

The desktop app's **Upgrade now** (internal/update) trusts a release only through
`SHA256SUMS.ed25519`: an Ed25519 signature over `"remohop-update-v1\n" + tag + "\n" + SHA256SUMS`,
made in the publish job with the repository secret `UPDATE_SIGNING_KEY`. Binding the tag stops an
older release's files from being served as a newer one. The app accepts only the public keys in
`internal/update/keys.go`; while that list is empty, upgrades stay off and the job only warns.

Setting it up (once, on the owner's machine, never in CI or a chat):

```bash
go run ./scripts/updsign keygen DIR        # prints the public key; DIR/update-signing.key is the private key
gh secret set UPDATE_SIGNING_KEY -R readori/RemoHop < DIR/update-signing.key
go run ./scripts/updsign keygen SPARE      # the spare: keep SPARE/update-signing.key offline
```

Both public keys go into `TrustedKeys` (the release key first). Once they are there, a release
without the secret fails instead of shipping unsigned. If the release key is lost or leaked, set the
secret to the spare's private key, publish a release whose `keys.go` drops the old key and adds a new
spare, and announce it; apps that upgraded to it no longer trust the old key.

## Windows signing (hook, not active)

Windows binaries ship unsigned (SmartScreen warns). The hook is `scripts/sign-windows.sh`, called by the
`build` job between the build and the zip step, so the signed files are the ones hashed and signed again by
cosign. It does nothing while the repository variable `WINDOWS_SIGNING` is unset.

Plan: [SignPath Foundation](https://signpath.org/) offers free code signing for open-source projects and requires
a public repository. When it is public: apply, create the project and a signing policy there, store the API token
as the secret `SIGNPATH_API_TOKEN` and the organization id as a variable, then implement `sign()` in
`scripts/sign-windows.sh` (or replace the hook step in `release.yml` with SignPath's GitHub action, uploading the two
`.exe` files as an artifact first and downloading the signed ones back into `dist/`), and set `WINDOWS_SIGNING=signpath`.
Until `sign()` exists, setting the variable makes the release **fail on purpose**, so nobody ships believing they are
signed. Then drop the "unsigned" notes from `packaging/windows/README.txt`, the README and `docs/security.md`.

## Package-manager manifests

Templates are in `packaging/templates/`. `scripts/gen-manifests.sh vX.Y.Z ASSETS_DIR OUT_DIR` fills versions, URLs and
hashes from the release's `SHA256SUMS`; the release workflow runs it for stable tags and keeps the result as the
artifact `package-manager-manifests`. Values that need a human decision stay as `REPLACE_ME...` markers (the script lists
them): today only the AUR maintainer. The winget publisher defaults to `RemoHop` (`WINGET_PUBLISHER`,
`WINGET_PUBLISHER_NAME` override it).

| Output | Goes to | Notes |
|---|---|---|
| `homebrew-tap/Formula/remohop.rb` | `readori/homebrew-tap` | `brew install readori/tap/remohop`; tests `remohop version`. Add `brew test-bot` in the tap's CI. |
| `homebrew-tap/Casks/remohop-quick.rb` | `readori/homebrew-tap` | `brew install --cask readori/tap/remohop-quick`: RemoHop.app, the desktop app (the token is from when it was quick support, kept so that installs upgrade). Not called `remohop`, so it is not confused with the formula. `zap` removes only the app's own files (`desktop.*`, `webview`, `quick.*`, the login item), never the CLI's identity. |
| `scoop-bucket/bucket/remohop.json` | `readori/scoop-bucket` | `scoop bucket add readori https://github.com/readori/scoop-bucket`; has `checkver`/`autoupdate`, so the bucket's Excavator bot can follow releases. |
| `winget/manifests/r/RemoHop/RemoHop/<ver>/*.yaml` | PR to `microsoft/winget-pkgs` | `RemoHop.RemoHop`, portable zip installer; validate with `winget validate --manifest DIR`; `wingetcreate update` can automate later PRs. |
| `aur/remohop-bin/{PKGBUILD,.SRCINFO}` | `ssh://aur@aur.archlinux.org/remohop-bin.git` | prebuilt binaries; regenerate `.SRCINFO` with `makepkg --printsrcinfo` on Arch before pushing. |
| `nix/remohop.nix` | your overlay/flake, later nixpkgs | binary package (fetchurl); a source `buildGoModule` package is better for nixpkgs once the repo is public. |

The tap and the bucket are live and are updated by hand after each release (commit the rendered files); until
then they install the previous version. The snap's pinned release moves with `packaging/snap/bump.sh vX.Y.Z
--verify require`. The `.deb/.rpm/.apk` files are attached to the release and not in a repository; an apt/dnf
repository is future work.

## Docker image

`packaging/docker/` has the Dockerfile (scratch, non-root UID 65532, read-only friendly, `/healthcheck`
probe in `packaging/docker/healthcheck/`), `compose.yaml` and the user-facing `README.md`. The release workflow builds
`linux/amd64` and `linux/arm64` with `docker buildx build` (no third-party actions, no QEMU), pushes to ghcr.io and signs
the digest with cosign. Every image the build uses is pinned by digest: the `# syntax=` line and `GO_IMAGE` in the
Dockerfile, BuildKit in `release.yml`. Raise `GO_IMAGE` (tag and digest) with go.mod's `go` line; take the digest of
the multi-platform index (`docker buildx imagetools inspect golang:<tag>`). After the first push, set the package visibility to public in the GitHub package settings.
The probe imports `internal/signal` and `internal/nat`; if their `Dial`/`Listen` signatures change, update
`packaging/docker/healthcheck/main.go` (`go vet ./...` catches it).

## Building everything locally

```bash
VERSION=v0.0.0-test.1 OUT=$PWD/dist/release scripts/build-release.sh all   # linux + windows
# macOS cannot be built into the universal signed binary here; to rehearse the wrap-up with any file:
VERSION=v0.0.0-test.1 OUT=$PWD/dist/release scripts/build-release.sh darwin ./some-remohop-binary
scripts/finalize-release.sh dist/release                                    # installers + SHA256SUMS
ALLOW_PRERELEASE=1 scripts/gen-manifests.sh v0.0.0-test.1 dist/release dist/manifests
cd dist/release && python3 -m http.server 8000 &                            # a fake release
BINDIR=/tmp/remohop-bin sh packaging/install.sh --base-url http://127.0.0.1:8000
docker build -f packaging/docker/Dockerfile --build-arg VERSION=v0.0.0-test.1 -t remohop-signal .
```

Requirements: Go (version in `go.mod`), `make`, GNU `tar`, `zip`, `jq`; nfpm is installed with `go install` at a
pinned version when it is not on `PATH`. No goreleaser: the pipeline is plain scripts so that every step is readable
and nothing has to be downloaded and trusted besides pinned cosign.

## Upgrades and removal

What each channel does with a running host (user-facing text: [docs/en/install.md](../docs/en/install.md#upgrading)):

- `.deb`/`.rpm` (`packaging/scripts/`): a first install enables nothing. An upgrade restarts a running
  `remohop.service` (`systemctl try-restart`), unless an ancestor of the maintainer script is a `remohop`
  process (the upgrade runs in a RemoHop shell, which the restart would cut off): then it prints how to restart.
  It lists remohop processes still running the replaced binary. Removal stops and disables the system unit and says
  how to remove a per-user one; a deb purge deletes `/var/lib/remohop`. `remohop service` sees the packaged units
  once they are enabled.
- `install.sh`/`install.ps1`: replace the file and say when a running service keeps the old version.
- Windows service: `remohop service install` again is the upgrade (stop, replace the copy in Program Files,
  update in place, start).
- Homebrew/Nix: the service names the stable link it was started by, which upgrades keep.

## Not done yet

- Windows code signing (hook only), see above.
- apt/dnf repositories, a Homebrew core submission, Chocolatey, Flatpak.
- `filippo.io/cpace` (the password login's PAKE) is an unreleased module its author calls experimental; see the
  note in `go.mod`. Vendoring it with a recorded review, or moving to an implementation of the current CPace
  draft with test vectors, is open.
- The Microsoft Store package of quick support: built and install-tested in CI, not submitted yet; see
  [windows/msix/README.md](windows/msix/README.md).
