# Files

[中文](../zh/files.md)

Every remote command has the form `remohop <ID> <command>`. `dev` is the example alias from `remohop <ID> pair --name dev` (see [Quick start](quick-start.md)). Run `remohop help <command>` for the full syntax.

Remote paths start with `:`:

```bash
remohop dev ls -l :/var/log
remohop dev cp ./build.tar.gz :/tmp/
remohop dev cp -rp :project .
remohop dev send report.pdf
remohop dev sync -n --delete --exclude node_modules ./site :/var/www/site
```

- `cp` and `sync` resume an interrupted file of 8 MiB or more when you run the same command again (`--no-resume` starts over). `sync --perms` copies permission bits exactly. When uploading, the host names the hidden partial file itself, from the source, the destination and the session's credential; with a [grant](../agents.md#grants) a reissued credential starts over, and clients older than this cannot resume under a grant (they send the whole file).
- Under a grant with file roots, everything happens inside the roots: links and junctions that lead out are not followed, links created must point inside the same root, the root folder itself cannot be removed or replaced, and moving or linking between two different roots fails with `EXDEV` (`mv` then copies).
- `send` delivers files to the remote user's Downloads folder and never overwrites: a taken name becomes `name (1).ext`.

Mount a remote directory so local editors and build tools use it directly:

```bash
remohop dev mount :~/src/app ~/mnt/app
remohop umount ~/mnt/app
```

Linux mounts with FUSE. macOS and Windows mount through the operating system's own WebDAV client, with nothing to install; on Windows the target is a drive letter (`Z:`, or `*` for a free one). `-o nomount` serves WebDAV on a loopback URL for any client. See [troubleshooting.md](../troubleshooting.md#mounts).
