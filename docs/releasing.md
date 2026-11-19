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

正式分发的版本号不使用 dev 后缀（如 0.1.0）。把该版本的全部改动提交后，为对应提交打标签（如 `git tag v0.1.0`），源码包用同一提交生成，便于接收者复核发布来源。

## 分发前的检查清单

正式向班级或课题组分发前，逐项确认：

1. 版本号不含 dev 后缀，全部改动已提交并打上对应的 Git 标签。
2. README 与包内「使用说明.txt」的非官方声明与免责声明仍然准确。
3. 安装包由 `make release` 生成，且 `make check-release` 全部通过。
4. 先在小范围试用：观察学校系统是否触发风控或限流，收集登录、WebVPN 与各页面的问题，再扩大分发。
5. 向使用者说明升级途径：解压新版 ZIP 后重新运行 `install.sh` 与 `bupt init`。

## 分发后的维护责任

把安装包发给师生，同时意味着维护者承诺持续适配学校系统变化。统一身份认证页面、WebVPN 与平台接口改版后，登录或功能可能失效，需要及时修复并发布新版，再通知使用者升级。无法继续维护时应停止分发，并在说明中写明最后支持的版本。

## 生成源码包

提交当前源码和 `VERSION` 后运行 `make source`。它用 `git archive` 将 HEAD 归档到 `dist/bupt-cli-版本号-source.zip`。需要复核发布来源时，可用与该提交对应的 Git 标签。源码包可独立运行 `make test` 和 `make release`。
