# Licensing

RemoHop is dual-licensed.

## Open source: GNU AGPL-3.0

Everyone may use, study, modify and redistribute RemoHop under the terms of the
GNU Affero General Public License, version 3 only (AGPL-3.0-only), whose full
text is in [LICENSE](LICENSE).

In short, and only as a reading aid (the license text is what applies):

- You may run RemoHop for any purpose, including commercially.
- If you distribute RemoHop, modified or not, you must make the corresponding
  source code available under the AGPL-3.0.
- **Network use counts.** If you modify RemoHop and let other people use it over
  a network, for example by running a modified `remohop signal` rendezvous server
  or a modified `remohop web` gateway for others, you must offer those users the
  source code of your modified version (section 13 of the AGPL-3.0).
- Running an unmodified RemoHop, or using it only yourself, adds no obligation
  beyond the license's notices.

## Commercial license

If the AGPL-3.0 does not fit your use, for example to ship RemoHop inside a
closed-source product, to run a modified server for others without publishing
your changes, or to distribute it through a store whose terms conflict with
the AGPL, a commercial license is available from the copyright holder,
readori.

For commercial licensing, write to <support@readori.com> and describe how you
plan to use RemoHop.

## Contributions

Because RemoHop is offered under a commercial license as well as the AGPL-3.0,
contributions can only be accepted under a Contributor License Agreement that
lets the copyright holder relicense them. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Third-party software

RemoHop links Go modules under the MIT, BSD-2-Clause, BSD-3-Clause, ISC and
Apache-2.0 licenses, all compatible with the AGPL-3.0. Release archives include
their license texts as `THIRD_PARTY_LICENSES.txt`, produced by
[`packaging/third_party_licenses.sh`](packaging/third_party_licenses.sh).

RemoHop does not include or distribute ffmpeg. The remote desktop runs the
`ffmpeg` already installed on the host as a separate program. Anyone who
bundles an ffmpeg build with RemoHop takes on that build's own license terms:
ffmpeg with libx264 is under the GPL.
