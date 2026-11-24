# 网状网络与 Tailscale

[English](../en/mesh-and-tailscale.md)

## 网状网络（remohop net）

`remohop net up` 把账号里的所有设备（见[账号](accounts.md)）连成一个私有网络，相当于只给自己设备用的小型 Tailscale：每台设备得到一个由身份密钥推导出的固定地址（`10.78.x.y` 和一个 IPv6 唯一本地地址），以及一个名字，例如 `nas.remohop`。

```bash
sudo remohop net up
remohop net peers
ping nas.remohop
sudo remohop net up --route 192.168.1.0/24 --exit     # 办公室的机器
sudo remohop net up --accept-routes --use-exit office  # 笔记本
remohop net policy set policy.json
sudo remohop net down
```

- 设备之间的链路就是普通的 RemoHop 会话，有流量时才建立（先打洞，不行再走中继或 websocket 桥），有保活，设备重启或换网络后会重新建立。这种登录只限于网状网络：同一账号的设备并不因此获得对方的 shell 或文件。
- 签名的访问策略（哪台设备可以访问哪些端口）在设备之间传递，由接收方执行。没有策略时，每台设备可以访问其他设备的所有端口。
- 网状网络运行在被控端守护进程里（需要 root 或 `CAP_NET_ADMIN`），随被控端服务一起启动。目前只支持 Linux；子网路由和出口节点只支持 IPv4。
- 对系统 DNS 的改动尽量保守：有 systemd-resolved 时只为 `.remohop` 设置按接口的分流 DNS，否则只在要求时（`--dns hosts`）改动。

详细说明、测试范围，以及没有复刻的 Tailscale 功能（单点登录、与他人共享、管理后台、Funnel 等）见 [net.md](../net.md)（英文）。

## 与 Tailscale 一起使用

RemoHop 会检测正在运行的 Tailscale（本地 API、`tailscale status --json` 或它的网络接口），但不依赖它。两台机器在同一个 tailnet 上时，tailnet 地址作为额外的候选路径，优先于打洞，并且在中继之前尝试；这时 `remohop <ID> ping` 显示 `tailnet path`。走这条路径的仍然是 RemoHop 自己的会话，和其他路径一样端到端加密并经过认证。`remohop doctor` 会解释当前的网络情况，包括 Tailscale；设置 `REMOHOP_TAILSCALE=off` 可以让 RemoHop 忽略 Tailscale。见 [net.md](../net.md#using-remohop-with-tailscale)。
