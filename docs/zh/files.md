# 文件

[English](../en/files.md)

远程命令的格式都是 `remohop <ID> <command>`。本页的例子用 `dev` 作为别名，它来自 `remohop <ID> pair --name dev`（见[快速开始](quick-start.md)）。每个命令的完整语法用 `remohop help <command>` 查看。

远程路径以 `:` 开头：

```bash
remohop dev ls -l :/var/log
remohop dev cp ./build.tar.gz :/tmp/
remohop dev cp -rp :project .
remohop dev send report.pdf
remohop dev sync -n --delete --exclude node_modules ./site :/var/www/site
```

- `cp` 和 `sync` 传输 8 MiB 及以上的文件中断后，重新执行同一条命令会接着传（`--no-resume` 则从头开始）。`sync --perms` 原样复制权限位。上传时，隐藏的续传临时文件由被控端根据源文件、目标和会话凭据自行命名；使用[授权](../agents.md#grants)时，重新签发的凭据会从头开始传，早于此版本的客户端在授权下不能续传（改为整份上传）。
- 在限定目录的授权下，所有文件操作都限制在这些目录之内：指向目录外的符号链接和 Windows 目录联接（junction）不会被跟随，新建的链接必须指向同一个授权目录之内，授权目录本身不能被删除或替换，在两个不同授权目录之间移动或建立硬链接会返回 `EXDEV`（`mv` 会改为复制）。
- `send` 把文件投递到远程用户的“下载”目录，从不覆盖：重名时保存为 `name (1).ext`。

把远程目录挂载到本地，让编辑器和构建工具直接读写：

```bash
remohop dev mount :~/src/app ~/mnt/app
remohop umount ~/mnt/app
```

Linux 用 FUSE 挂载。macOS 和 Windows 通过操作系统自带的 WebDAV 客户端挂载，无需安装任何东西；Windows 上挂载点是盘符（`Z:`，或用 `*` 自动选一个空闲盘符）。`-o nomount` 只在本机回环地址上提供 WebDAV 服务，供任意客户端使用。见 [troubleshooting.md](../troubleshooting.md#mounts)。
