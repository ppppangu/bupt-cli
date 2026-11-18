# 构建与分发

在 macOS 或 Linux 上从仓库根目录运行。Go 版本以 `go.mod` 为准。维护者打包还需 Make、Python 3.8 或更新版本、`sha256sum` 或 `shasum`。使用预编译安装包的师生只需终端和系统的 SHA-256 工具。

## 生成师生包

修改版本时更新仓库根目录的 `VERSION`。为同一版本生成可比较的产物时，使用同一源码和 Go 工具链。

```sh
make test
make release
make check-release
```

默认在 `dist/` 生成 `bupt-版本号.zip` 和 `bupt-版本号.zip.sha256`。把这两个文件放在同一个下载位置或发给班级。接收者用 Finder、资源管理器或 `unzip` 完整解压，按包内的「使用说明.txt」安装。校验整个 ZIP 时，在这两个文件所在的目录运行下列命令之一，将「版本号」换成实际值。

```sh
shasum -a 256 -c bupt-版本号.zip.sha256
sha256sum -c bupt-版本号.zip.sha256
```

包内的 `SHA256SUMS` 记录展开后各文件的散列。安装脚本自动校验即将写入的二进制，用临时文件完成替换。需要自选安装路径时可用 `sh install.sh --bin-dir 目录`。同目录的原始四平台二进制和 `dist/SHA256SUMS` 也可单独用于控制器升级。

`make check-release` 检查压缩包内容、内外校验和、四种平台选择及安装失败时对旧程序的保护。包内第三方声明从参与编译的 Go 依赖和工具链读取。当前分发使用 ZIP 和 SHA-256，未办理 Apple Developer ID 签名与公证。

## 生成源码包

提交当前源码和 `VERSION` 后运行 `make source`。它用 `git archive` 将 HEAD 归档到 `dist/bupt-cli-版本号-source.zip`。需要复核发布来源时，可用与该提交对应的 Git 标签。源码包可独立运行 `make test` 和 `make release`。
