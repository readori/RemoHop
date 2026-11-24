# Clipboard

[中文](../zh/clipboard.md)

Every remote command has the form `remohop <ID> <command>`. `dev` is the example alias from `remohop <ID> pair --name dev` (see [Quick start](quick-start.md)). Run `remohop help <command>` for the full syntax.

Keep the local and remote clipboards in sync (text and images), or read and set the remote one once:

```bash
remohop dev clip sync
remohop dev clip -b --pull-only
remohop dev clip set "hello"
remohop dev clip get -o shot.png
```

Everything that passes through is recorded in a local history, numbered from the newest:

```bash
remohop clip history -n 5
remohop dev clip history show 2
remohop dev clip history send 2
remohop dev clip history --remote
remohop clip history -n 20 --md
```

`history` also has `copy`, `rm`, `clear`, `on|off`, `take` and `restore`; see `remohop help clip`.
