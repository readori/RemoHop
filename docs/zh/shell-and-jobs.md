# shell 与后台任务

[English](../en/shell-and-jobs.md)

远程命令的格式都是 `remohop <ID> <command>`。本页的例子用 `dev` 作为别名，它来自 `remohop <ID> pair --name dev`（见[快速开始](quick-start.md)）。每个命令的完整语法用 `remohop help <command>` 查看。

交互式 shell 是持久的：你断开后会话仍在被控机上运行，重新连接时会回放你错过的输出。

```bash
remohop dev shell
```

先按回车，再输入 `~.` 即可分离。分离后的会话默认在被控机上保留 30 分钟（由被控端的 `term-keep` 设置决定）。用 `remohop dev shell` 重新连接；用 `remohop dev shell -s build --new` 另开一个会话。

执行单条命令，或者启动一个在你断开后仍继续运行的后台任务：

```bash
remohop dev shell -c 'make test' --cwd src/app --env CI=1 --timeout 10m
remohop dev spawn --name build --cwd src/app make -j8
remohop dev logs -f build
remohop dev jobs
remohop dev kill build
```

被控机是 Windows 时，shell 是 PowerShell，命令要按 PowerShell 的语法写。
