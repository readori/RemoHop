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
| `remohop_<ver>_{amd64,arm64}.deb`, `remohop-<ver>-1.{x86_64,aarch64}.rpm`, `remohop_<ver>_{x86_64,aarch64}.apk` | nfpm, from `packaging/nfpm.yaml` |
| `remohop_sbom.cdx.json` | `scripts/sbom.sh` (CycloneDX, from `go version -m`) |
| `install.sh`, `install.ps1` | copied from `packaging/` by `scripts/finalize-release.sh` |
| `SHA256SUMS` | `finalize-release.sh`, over every file above |
| `SHA256SUMS.sigstore.json` | cosign keyless signature bundle over `SHA256SUMS` |

Also published, not as release assets: the image `ghcr.io/<owner>/remohop-signal:<tag>` (multi-arch,
signed, SBOM + provenance attached), and a workflow artifact `package-manager-manifests` (stable tags only).

Every archive contains the binary, `LICENSE` (AGPL-3.0) and `THIRD_PARTY_LICENSES.txt`
(`packaging/third_party_licenses.sh`). Archives and the SBOM are reproducible: the same commit
and Go toolchain give byte-identical files (`SOURCE_DATE_EPOCH` is the commit time).

## Cutting a release

1. Merge to `main`; make sure CI is green and `docs/` match.
2. Optional dry run: Actions, "Release", Run workflow, version `v0.0.0-test.1`. It builds and keeps the
   files as the artifact `assets-linux-windows`; it never signs, releases or pushes.
3. Tag and push: `git tag -s v1.2.3 -m v1.2.3 && git push origin v1.2.3`. Tags with a `-` (`v1.2.3-rc.1`)
   become prereleases and are not tagged `latest` in Docker or rendered into manifests.
4. The workflow runs `build`, then `macos` (starts the signing workflow in the CI repo and waits, about
   10 to 20 minutes), then `publish` (draft release, signature, publish), then `docker`.
5. Verify (below), then submit the package-manager files (below).

If `macos` fails, nothing is released; re-run the failed jobs. If only `docker` fails, the release is out;
re-run that job.

### Secrets and variables

| Name | Where | Needed for |
|---|---|---|
| `CI_REPO_TOKEN` (secret) | RemoHop repo | the `macos` job: a fine-grained personal access token, resource owner `readori`, **only** repository `test-env-setup`, permission "Actions: Read and write". Lets it start and read runs of the signing workflow and download its artifact. Nothing else. Set an expiry and rotate it. |
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
  (`ref=<commit>`, `version=<tag>`, `upload=true`, `correlation=<id>`); with `upload=true` it also uploads the
  signed binary as the artifact `remohop-macos-universal` (3 days). The release job downloads it, checks the
  recorded hashes, that it is a universal Mach-O and carries the version string, and wraps it into
  `remohop_darwin_universal.tar.gz`. Requests that do not set `upload` upload nothing.
- *Alternative:* turn the CI workflow into a reusable workflow (`on: workflow_call`) and call it from
  `release.yml` with `uses: readori/test-env-setup/.github/workflows/sodesk-macos-release.yml@<sha>`. That removes
  the polling, but the Apple secrets would then have to be present in (or inherited by) the RemoHop repo, which
  is exactly what the dispatch design avoids. Revisit if the polling ever becomes a problem.

## Checksums, signature and provenance

- **cosign keyless** signs `SHA256SUMS`, because it needs no long-lived secret: the job gets a short-lived
  GitHub OIDC token, Sigstore's Fulcio issues a certificate naming this repository, workflow file and tag, and the
  signature is recorded in the public Rekor log. There is no private key to leak, rotate or lose; what users trust
  is "this GitHub workflow, for this tag", which is what `install.sh` checks (`--certificate-identity-regexp` for
  `.github/workflows/release.yml@refs/tags/v*` and the GitHub issuer). The trade-offs: verifying needs cosign (the
  scripts continue on the checksum alone when it is missing, unless `--verify require`), and the identity includes
  the repository path, so a rename or transfer changes it (update the regexp in `install.sh`/`install.ps1`).
  minisign would work offline but needs a secret key in CI and a public key distributed out of band.
- Because the whole release hangs on `SHA256SUMS`, one signature covers every file and the installers.
- **cosign** itself is downloaded by `scripts/install-cosign.sh` as a pinned version whose SHA-256 is checked
  (hashes copied from the project's `cosign_checksums.txt`). To upgrade, change the version and both hashes together.
- **SBOM:** `remohop_sbom.cdx.json` lists the Go toolchain and every module linked into the release binaries
  (union over the four linux/windows builds; the macOS binary is the same source and module set). It is generated
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
  --certificate-identity-regexp '^https://github\.com/readori/RemoHop/\.github/workflows/release\.yml@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
```

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
them): `WINGET_PUBLISHER`/`WINGET_PUBLISHER_NAME` environment variables, the tap owner and name, the AUR maintainer.

| Output | Goes to | Notes |
|---|---|---|
| `homebrew-tap/Formula/remohop.rb` | a repo named `homebrew-<tap>` | `brew install <owner>/<tap>/remohop`; tests `remohop version`. Add `brew test-bot` in the tap's CI. |
| `scoop-bucket/bucket/remohop.json` | a repo used as a Scoop bucket | has `checkver`/`autoupdate`, so the bucket's Excavator bot can follow releases. |
| `winget/manifests/<l>/<Publisher>/RemoHop/<ver>/*.yaml` | PR to `microsoft/winget-pkgs` | portable zip installer; validate with `winget validate --manifest DIR`; `wingetcreate update` can automate later PRs. |
| `aur/remohop-bin/{PKGBUILD,.SRCINFO}` | `ssh://aur@aur.archlinux.org/remohop-bin.git` | prebuilt binaries; regenerate `.SRCINFO` with `makepkg --printsrcinfo` on Arch before pushing. |
| `nix/remohop.nix` | your overlay/flake, later nixpkgs | binary package (fetchurl); a source `buildGoModule` package is better for nixpkgs once the repo is public. |

None of these work before the first release is published from a public repository (until then the download URLs return 404). The `.deb/.rpm/.apk` files are
attached to the release and not in a repository; an apt/dnf repository is future work.

## Docker image

`packaging/docker/` has the Dockerfile (scratch, non-root UID 65532, read-only friendly, `/healthcheck`
probe in `packaging/docker/healthcheck/`), `compose.yaml` and the user-facing `README.md`. The release workflow builds
`linux/amd64` and `linux/arm64` with `docker buildx build` (no third-party actions, no QEMU), pushes to ghcr.io and signs
the digest with cosign. After the first push, set the package visibility to public in the GitHub package settings.
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

## Not done yet

- Windows code signing (hook only), see above.
- `install.ps1` has not been run on Windows yet. Run it once against a dry-run release before the first release.
- apt/dnf repositories, a Homebrew core submission, Chocolatey, Flatpak/Snap.
- With the first release, update the "no release has been published yet" statements in `README.md`, `README.zh-CN.md`,
  `docs/zh/install.md`, `docs/en/install.md`, `docs/*/platforms.md`, `docs/*/mobile-apps.md` and `docs/security.md`.
