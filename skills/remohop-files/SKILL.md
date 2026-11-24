---
name: remohop-files
description: Copy, sync and mount files between this machine and a remote machine by RemoHop ID with `remohop <ID> ls`, `cp`, `send`, `sync` and `mount`. Use when you need to fetch a log, config or build artifact from another computer, push a file or project to it, mirror a directory, or browse a remote directory with local tools. No scp/SSH needed.
---

# RemoHop files

Remote paths start with a colon (`:/var/log/syslog`, `:notes.txt` is relative to the remote home, `:~/x` works). Local paths have no colon. Requires a paired host (`remohop <ID> pair`; see `skills/README.md`).

(With the MCP server: `remohop_list_dir`, `remohop_read_file`, `remohop_write_file`, `remohop_upload`, `remohop_download`, `remohop_send`.)

## Look around (`ls`)

```bash
remohop <ID> ls                      # the remote home: names, directories end in /
remohop <ID> ls -l :/var/log         # mode, size, mtime, symlink targets
remohop <ID> ls -l -a --json :src     # include dotfiles; JSON array of {name, type, size, mode, mtime, target}
```

## Hand a file to the remote user (`send`)

```bash
remohop <ID> send report.pdf               # lands in the remote user's Downloads folder
remohop <ID> send -r ./photos notes.txt    # directories need -r
remohop <ID> send --json build.zip         # [{source, path, bytes, dir}] with the final remote path
```
No remote path to choose and **nothing is ever overwritten**: a taken name becomes `name (1).ext`. Read the final path from the output. Use `cp` when the file must land at a specific path.

## Copy (`cp`)

```bash
remohop <ID> cp :/var/log/app.log /tmp/app.log                 # download one file
remohop <ID> cp :/var/log/syslog :/var/log/auth.log /tmp/logs/ # several into a directory
remohop <ID> cp ./build.tar.gz :/tmp/                           # upload
remohop <ID> cp -r ./site :/var/www/site                        # directory, recursive (-r)
remohop <ID> cp -rp :project .                                  # -p keeps modification times
echo hello | remohop <ID> cp - :/tmp/hello.txt                  # stdin -> remote file
remohop <ID> cp :/etc/os-release -                              # remote file -> stdout
```
Semantics follow `cp`: if the destination is an existing directory, sources go inside it. Either all sources are local or all are remote. Files are written under a temporary name and renamed, so an interrupted copy never leaves a truncated file. Add `-q` to silence progress and `--json` for a summary; `-j N` sets parallelism (default 8). Files of 8 MiB or more are resumable: re-run the same `cp` after an interruption and it continues where it stopped (`--no-resume` starts over).

To read a small remote file without keeping it, use the shell skill instead: `remohop <ID> shell -c 'head -n 100 /etc/app.conf'`.

## Mirror a directory (`sync`)

```bash
remohop <ID> sync -n --delete ./src :src               # DRY RUN first: shows what would change
remohop <ID> sync --delete --exclude node_modules --exclude '*.log' ./site :/var/www/site
remohop <ID> sync :/srv/app ./app                      # remote -> local
```
`dst` becomes an exact mirror of `src` (both name the directories themselves; trailing slashes do not matter). Exactly one side is remote. `--delete` removes files in `dst` that are absent from `src` - **always run with `-n` first** and review before deleting anything. `--checksum` compares content instead of modification times. Output lines: `+` transferred, `d` directory created, `~` metadata updated, `-` deleted.

## Mount (`mount`) - browse with local tools

```bash
remohop <ID> mount --ro :/var/log /tmp/remote-logs     # read-only is the safe default for inspection
grep -r "OutOfMemory" /tmp/remote-logs | head
remohop umount /tmp/remote-logs                         # always unmount when done
remohop ps                                              # shows running mounts
```
On Linux this needs FUSE (`/dev/fuse` and `fusermount3` for non-root); macOS and Windows use the system's WebDAV client instead (on Windows the target is a drive letter such as `Z:`, or `*` for a free one). On Linux and macOS the local directory must already exist. Mounts keep a background process alive: unmount them when finished. If mounting is unavailable, fall back to `cp` or remote `grep`.

## Safety habits

- Show the user the exact source and destination before overwriting remote files; `cp` replaces existing files silently.
- Downloads are confined to the destination, but do not execute downloaded files without the user's say-so.
- Never copy secrets (`~/.ssh`, `.env`, key stores) unless asked.
