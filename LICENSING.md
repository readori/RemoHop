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
- **Credit the project.** A work based on RemoHop must name RemoHop and link
  to https://github.com/readori/RemoHop (see the additional terms below).

## Additional terms under section 7

These terms are added to the AGPL-3.0 under its section 7. They apply to
everyone who uses RemoHop under the AGPL-3.0, and to every work based on
RemoHop: a fork, a modified version, or a product that includes RemoHop's
code, in source or object form.

1. **Attribution (section 7(b)).** A work based on RemoHop must name the
   project and link to its source repository, as in this notice:

   > Based on RemoHop (https://github.com/readori/RemoHop), © RemoHop,
   > licensed under the AGPL-3.0.

   The notice must appear
   - in the source code: keep the [NOTICE](NOTICE) file, and put the notice
     in the README or an equally visible file of your distribution; and
   - wherever the work shows its Appropriate Legal Notices (AGPL-3.0 section
     0 and 5(d)), for example an About or Settings screen, the output of a
     `version` or `help` command, or the footer of a web interface.

   Do not remove or hide existing copyright, license and attribution notices.
2. **Modified versions (section 7(c)).** A modified version must be marked
   as changed, and must not claim to be the original RemoHop or suggest that
   the RemoHop project made, endorses or supports it.
3. **Names and logos (section 7(e)).** The AGPL-3.0 does not grant any right
   to use the name "RemoHop", or its logos, as the name, logo
   or branding of another product or service, apart from the attribution
   required in term 1. Give your work its own name.

Under section 7, anyone who receives RemoHop may not remove these terms when
passing it on. They do not apply to software you receive under a commercial
license; that license states its own terms.

### 依据第 7 条的附加条款（中文说明）

以上条款依据 AGPL-3.0 第 7 条附加于许可证之上，适用于所有按 AGPL-3.0 使用
RemoHop 的人，以及所有基于 RemoHop 的作品（分支、修改版，或包含 RemoHop
代码的产品，无论源代码还是编译后的形式）。如有歧义，以上方英文为准。

1. **署名（第 7(b) 条）。** 基于 RemoHop 的作品必须标明本项目名称和开源地址，
   例如：“基于 RemoHop（https://github.com/readori/RemoHop），© RemoHop 项目，
   按 AGPL-3.0 授权。”这段说明必须出现在：
   - 源代码中：保留 [NOTICE](NOTICE) 文件，并写进 README 或同样醒目的文件；
   - 作品展示“适当法律声明”的地方（AGPL-3.0 第 0 条和第 5(d) 条），例如
     “关于”或“设置”页面、`version` 或 `help` 命令的输出、网页界面的页脚。

   不得删除或隐藏已有的版权、许可证和署名声明。
2. **修改版（第 7(c) 条）。** 修改版必须标明已被修改，不得自称是原版 RemoHop，
   也不得暗示由 RemoHop 项目制作、认可或提供支持。
3. **名称和标志（第 7(e) 条）。** AGPL-3.0 不授予把“RemoHop”名称或
   其标志用作其他产品或服务的名称、标志或品牌的权利（第 1 条要求的署名除外）。
   请为你的作品另取名字。

依据第 7 条，接收 RemoHop 的人在再分发时不得删去这些条款。通过商业授权获得的
软件不适用这些条款，以商业授权协议的约定为准。

## Commercial license

If the AGPL-3.0 does not fit your use, for example to ship RemoHop inside a
closed-source product, to run a modified server for others without publishing
your changes, or to distribute it through a store whose terms conflict with
the AGPL, a commercial license is available from the copyright holder,
the RemoHop project.

For commercial licensing, write to <info@remohop.com> and describe how you
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

RemoHop does not include or distribute ffmpeg. The remote desktop runs
`ffmpeg` on the host as a separate program: one already installed there or,
on Windows and macOS when the user asks for it (`remohop ffmpeg install`,
the button in the desktop app and the Quick Support window, or turning on
unattended access in the desktop app on Windows, for the service), a build that
the user's computer downloads from its publisher (gyan.dev, BtbN's
FFmpeg-Builds, Martin Riedl's build server) at an address and SHA-256 pinned
in `internal/ffmpeg/builds.go`. Those builds are under the GPL (they include
libx264), and come with their own license terms and source offers from
their publishers. Anyone who bundles an ffmpeg build with RemoHop takes on
that build's own license terms: ffmpeg with libx264 is under the GPL.
