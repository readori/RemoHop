# 移动端应用

[English](../en/mobile-apps.md)

仓库里有两个移动端应用：`mobile/android`（Android）和 `mobile/ios`（iOS）。它们都是**控制端**：一个原生外壳加一个 WebView，WebView 显示进程内运行的 `remohop web` 网关（Go 核心通过 gomobile 绑定）。

- 只是控制端。把 Android 或 iOS 设备作为被控机没有实现。
- 目前没有商店发布版本，需要自己构建。
- Android：运行 `mobile/android/build.sh`（需要 Go、JDK 17+、Android SDK 和 NDK，`ANDROID_HOME` 要设置），默认生成 debug APK 并运行 JVM 单元测试。
- iOS：在 macOS 上先运行 `mobile/ios/build-xcframework.sh` 生成 `Remohopcore.xcframework`，再用 Xcode 构建 `mobile/ios/RemoHop.xcodeproj`。
- 已测试：iOS 应用在 CI 的 iOS 模拟器中运行，应用内的测试用地址和密码连接一台 `remohop host`，在终端里执行一条命令，并在“文件”里列出一个目录。没有在实体 iPhone 或 iPad 上运行过，也没有发布过签名的构建。Android 应用没有在真机上测试过。
- 移动端应用目前不支持账号登录。
- 界面语言：原生界面和网页部分都有简体中文和英文。默认跟随系统（系统语言是任何一种中文时显示中文，否则显示英文）；在“设置 > 语言”里切换，立即生效，已建立的会话不会断开。Android 13 及以上版本中，系统的“应用语言”设置与此是同一个选项。
