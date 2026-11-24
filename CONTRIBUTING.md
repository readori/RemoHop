# Contributing to RemoHop

Thank you for helping. Please read this before opening a pull request.

## License of contributions

RemoHop is dual-licensed: under the AGPL-3.0 for everyone, and under a
commercial license sold by the copyright holder (see
[LICENSING.md](LICENSING.md)). To keep both possible, every contribution must
be covered by a signed Contributor License Agreement (CLA). The CLA lets the
copyright holder distribute your contribution under both licenses. You keep
the copyright in your work.

Pull requests from authors without a signed CLA cannot be merged. To sign the
CLA, or before you start on a larger change, write to <info@remohop.com>.

## Before you send a change

Build and test commands are described in [docs/en/building.md](docs/en/building.md).

- Run the checks the CI runs:
  ```bash
  make fmt-check vet
  CGO_ENABLED=1 go test -race ./...
  ```
- Changes to NAT traversal should also pass the kernel NAT lab (needs root):
  ```bash
  sudo test/natlab/natlab.sh all
  ```
- Keep the binary pure Go (`CGO_ENABLED=0`) and make sure it still builds for
  linux, darwin and windows (`make cross`).
- Treat every message from a peer as untrusted, on both the host and the
  client side.
- Report security problems privately to the maintainers at
  <info@remohop.com>, not in a public issue.
