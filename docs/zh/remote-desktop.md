# 屏幕与远程桌面

[English](../en/remote-desktop.md)

远程命令的格式都是 `remohop <ID> <command>`。本页的例子用 `dev` 作为别名，它来自 `remohop <ID> pair --name dev`（见[快速开始](quick-start.md)）。每个命令的完整语法用 `remohop help <command>` 查看。

截取整个显示器或其中一块区域：

```bash
remohop dev screenshot -o /tmp/screen.png
remohop dev screenshot --max-width 1280 --format jpeg --json
remohop dev screenshot -R 0,0,800,600 -o /tmp/corner.png
```

在浏览器标签页中查看并控制远程屏幕：

```bash
remohop dev desktop
remohop dev desktop --max-height 1080 --fps 30
remohop dev desktop --codec h264 --record meeting.mp4
remohop dev desktop --audio --display all
remohop dev desktop --bench --bench-duration 20s
```

- **编码。** 被控机有 GPU AV1 编码器时用 AV1；否则用低延迟的 H.264，能硬件编码就硬件编码，不能则用 libx264。在只有 CPU 的主机上，H.264 的延迟约为软件 AV1 的一半或更低。`--codec av1` 强制使用 AV1。
- **查看器。** 页面上有按钮可以发送浏览器自己会拦截的组合键（Ctrl+Alt+Del、Alt+Tab、Windows 键），还可以输入任意 Unicode 文本、切换画质预设、切换显示器、锁定远程屏幕、传输剪贴板、上传文件、音频和录制。
- **录制。** `--record FILE.mp4`（或页面上的录制按钮）把收到的视频流写成本机上的分片 MP4，不重新编码。
- **音频。** `--audio`（或页面上的“Audio”按钮；“Mute”只静音、不停止传输）转发被控机正在播放的声音：被控机用 ffmpeg 采集系统输出，编码为 Opus（libopus，低延迟模式，20 ms 帧，96 kbit/s），在视频旁边的独立流上发送，所以视频帧晚到不会拖慢声音。页面用 WebCodecs 解码，通过 Web Audio 配合抖动缓冲播放，每个包按其采集时间和视频自身的延迟来安排播放，使画面与声音保持同步。音频默认关闭、需要主动请求；它属于被控机的 `screen` 功能（`screen` 关闭的主机不提供音频），也可以在被控机上用 `REMOHOP_DESKTOP_AUDIO=off` 单独关闭；只会采集机器的输出，绝不采集麦克风。被控机无法采集时，`--audio` 会停止并说明原因和需要安装什么，页面上的按钮也会显示同样的信息。被控机的要求：

  | 被控机 | 如何采集输出 |
  |---|---|
  | Linux | 运行 `remohop` 的进程能连上 PulseAudio，或带 `pipewire-pulse` 的 PipeWire（录制默认输出的 monitor），且 ffmpeg 带 libpulse 与 libopus。已在 PulseAudio null sink 上测试。 |
  | Windows | ffmpeg 若带 WASAPI loopback 则用它，否则用 DirectShow 回环设备：`virtual-audio-capturer`（免费的 screen-capture-recorder 驱动）或声卡启用后的“立体声混音”。每个候选项使用前都会试运行。未在真实 Windows 机器上验证。 |
  | macOS | macOS 无法录制自身的输出：需安装 BlackHole 之类的回环驱动，在“音频 MIDI 设置”中创建包含扬声器和 BlackHole 的“多输出设备”并设为输出，还要给 `remohop` 麦克风权限。没有这类设备时 `--audio` 会如实说明；绝不会自动选用麦克风。未在真实 Mac 上验证。 |

  被控机上的 `REMOHOP_DESKTOP_AUDIO_SOURCE` 可指定其他音源（PulseAudio 源、DirectShow 设备、AVFoundation 设备名或序号）。
- **显示器。** 页面的显示器菜单可在会话进行中切换：单个显示器，或**全部显示器**（整个虚拟桌面合成一幅画面，显示器大小和位置不同也可以，空隙为黑色）。指针输入会映射到当前显示的区域。`--display all` 或 `--display N` 指定起始画面；命令会列出被控机的显示器。Linux 上“全部显示器”就是整个 X11 根窗口；Windows 上是用 GDI 采集虚拟桌面的外接矩形（Desktop Duplication 一次只处理一个显示器，所以这个视图更耗 CPU；未在多显示器的 Windows 机器上验证）。macOS 没有“全部显示器”：AVFoundation 一次只采集一个显示器，被控机会如实报告而不是假装提供。
- **文件。** 把文件从文件管理器拖到页面上，或用页面的 Upload 按钮，即可复制到被控机用户的下载目录（`--upload-dir DIR` 可指定被控机上的其他目录），每个文件有一行进度。它走被控机的文件服务，因此需要 `file` 功能；绝不覆盖（已存在的名字会加 ` (1)`）；只读会话中会被拒绝。限制：只支持文件（不支持文件夹，请先压缩）；不支持文件的复制粘贴。页面把 Ctrl+V 交给远程机器，远程剪贴板只能承载文本和图片、没有文件列表，而浏览器只通过粘贴事件暴露粘贴的文件，这个事件会被远程键盘捕获取消。请先拖放或上传，再在远程的文件管理器中粘贴文件路径。
- **性能数据。** `remohop <ID> desktop --bench`（可加 `--bench-duration 20s`、`--audio`、`--json`）不开浏览器，直接打开一个会话，报告帧率、视频码率和采集到接收的延迟（被控机时钟到到达时刻，使用会话的时钟偏移估计），以及被控机自身的流水线延迟和丢帧。在一个 Linux 容器中的实测（4 vCPU，ffmpeg 6.1.1，Xvfb 1280x720 且画面持续变化，两端之间是回环 QUIC，命令 `REMOHOP_BENCH_REPORT=1 go test ./internal/svc/desktop -run TestBenchReportXvfb -v`）：

  | 流 | 帧率 | 视频码率 | 采集到接收（均值，p95） |
  |---|---|---|---|
  | H.264 libx264，60 fps | 60.0 | 2.4 Mbit/s | 5.9 ms，8.5 ms |
  | H.264 libx264，30 fps | 30.0 | 1.5 Mbit/s | 6.7 ms，9.0 ms |
  | AV1 SVT-AV1（软件），30 fps | 30.0 | 1.7 Mbit/s | 70.0 ms，72.1 ms |

  这些数字止于帧到达查看器进程的时刻，而 x11grab 在抓取时才给帧打时间戳，比屏幕变化最多晚一个帧周期。从屏幕变化到画面出现的真实数字在无头 Chromium 157 中测得（H.264，800x300，30 fps；测试让远程屏幕上的一块区域闪烁，页面里的 `requestAnimationFrame` 循环记录画布何时显示出来，所以包含最多 33 ms 的抓取等待和最多 16 ms 的检测误差）：40 次闪烁的中位数 31 ms，p95 为 47 ms（`go test ./internal/svc/desktop -run TestViewerPageXvfb -v`）。96 kbit/s 的音频每秒增加 50 个包；它的采集时间是根据包的到达时间估计的（被控机假定采集加编码流水线为 37 ms），所以采集到接收均值 47 ms 主要就是这个假设，并非独立测量。真实链路还要加上自己的往返时间，GPU 编码器会改变编码部分的数字。

查看器只监听 `127.0.0.1`，地址中带有随机路径，按 Ctrl-C 结束。被控机需要带 H.264 或 AV1 编码器的 ffmpeg，见 [troubleshooting.md](../troubleshooting.md#ffmpeg-and-encoders)。

macOS 被控端采集需要“屏幕录制”权限，输入需要“辅助功能”权限。macOS 同一时间只把屏幕交给一个采集进程，所以一台 macOS 被控端的所有会话共用一路采集；如果共享采集在某台 Mac 上工作不正常，可在被控端设置 `REMOHOP_MAC_DIRECT_CAPTURE=1` 关闭它（每个会话各自采集）。

## 图形界面自动化

下面的命令操作远程的鼠标和键盘。坐标是所选显示器在原尺寸截图中的像素，左上角为 0,0：

```bash
remohop dev click 412 230
remohop dev move 800 12
remohop dev type "hello world"
remohop dev press ctrl+a Delete
remohop dev scroll 0 5
```

如果坐标是在缩小后的截图上量出来的，把那张图的尺寸传给 `--scale-from`：

```bash
remohop dev screenshot --max-width 1280 -o s.png
remohop dev click 640 360 --scale-from 1280x720
```

用 `-d` 指定显示器，取值是 `screenshot --list` 给出的序号或名称。不要用 `type` 输入密码等机密：这些文字会留在你的终端或代理的对话记录里。
