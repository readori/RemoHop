# 剪贴板

[English](../en/clipboard.md)

远程命令的格式都是 `remohop <ID> <command>`。本页的例子用 `dev` 作为别名，它来自 `remohop <ID> pair --name dev`（见[快速开始](quick-start.md)）。每个命令的完整语法用 `remohop help <command>` 查看。

保持本机和远程剪贴板同步（文本和图片），或者单次读取、设置远程剪贴板：

```bash
remohop dev clip sync
remohop dev clip -b --pull-only
remohop dev clip set "hello"
remohop dev clip get -o shot.png
```

经过的所有内容都会记录在本机的历史中，从最新一条开始编号：

```bash
remohop clip history -n 5
remohop dev clip history show 2
remohop dev clip history send 2
remohop dev clip history --remote
remohop clip history -n 20 --md
```

`history` 还有 `copy`、`rm`、`clear`、`on|off`、`take` 和 `restore` 等子命令，见 `remohop help clip`。
