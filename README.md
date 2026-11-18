# bupt · 北邮算力命令行

面向北京邮电大学学生和教师的第三方工具。用自己的学校账号登录「人人有算力」，可以在终端里查模型、余额和 API Key，也可以选算力资源、管理实例和作业。输入 `bupt` 查看命令，输入 `bupt tui` 进入全屏界面。

## 收到安装包后

解压 `bupt-版本号.zip`，在包含 `install.sh` 的目录打开终端，依次执行以下命令。

```sh
sh ./install.sh
export PATH="$HOME/.local/bin:$PATH"
bupt init
bupt login
bupt tui
```

同一份 ZIP 适用于 Apple 芯片 Mac、Intel Mac 及 64 位 Linux（x86 或 ARM）。Windows 用户请在 WSL2 的 Linux 终端安装和使用。安装包自带程序，每位使用者在自己的设备上初始化和登录。[安装说明与 WSL2 用法](packaging/使用说明.txt)

`bupt init` 启动当前用户的本地控制器。校外网络认证过期时，`bupt login` 先显示 WebVPN 二维码，再完成统一身份认证。支持统一认证口令的账号可用 `bupt login --sso-password`。

## 常用操作

| 想做的事 | 命令 |
| --- | --- |
| 查模型 ID、上下文、价格和健康状态 | `bupt api model` |
| 查近 30 天的用量和余额 | `bupt api usage` |
| 查看我的 API Key | `bupt api key` |
| 查看可申请的算力模板与配额 | `bupt compute catalog` |
| 查看我的实例、预留和截止时间 | `bupt compute instance` |
| 查看平台登录和资源摘要 | `bupt status` |
| 退出当前配置的学校会话 | `bupt logout` |

初次使用可在首页打开「教程说明」，或运行 `bupt help tutorial`。桌面上多个 TUI 共用控制器的账号状态，在「账号状态与配置」可调整顶栏。个人配置和状态默认位于 `~/.bupt/`。

课堂演示可以直接打开 `bupt tui api` 或 `bupt tui compute`。方向键选择，Enter 打开，Esc 返回，`q` 退出。编写脚本时可用 `bupt api model --json` 取得结构化结果。实例启停、SSH、文件和后台作业的例子见[使用手册](docs/usage.md)。

### 在 TUI 里输入并发送

打开 `bupt tui api`，选择「试用模型 → 模型 ID」。输入框从空白开始。对话模型直接输入问题，向量模型输入一段或多段文本，重排模型填写问题和候选段落。输入时 Enter 换行，Ctrl+D 保存并发送一次，Ctrl+S 只保存。也可以选「填入示例」，修改后按「发送一次」。结果页按 Esc 可回到刚才的输入。

试用使用当前控制器已保管、已启用并授权此模型的 Key。仅一把符合条件时会自动选中，多把时可自行选择。学校按实际调用计量，浏览和编辑只读取管理数据。[命令行示例与结果格式](docs/usage.md#模型试用)

## 给班级或课题组分发

已有安装包时，把版本化的 ZIP 和同名 `.sha256` 校验文件一起转发即可。收到的人使用自己的配额和权限。

维护者从源码重新打包时，在 macOS 或 Linux 上准备 Go 1.26 和 Python 3，然后在仓库根目录执行以下命令。

```sh
make release
make check-release
```

版本号取自 `VERSION`。面向师生的文件是 `dist/bupt-版本号.zip` 和 `dist/bupt-版本号.zip.sha256`。压缩包内附安装脚本、中文使用说明、命令手册、第三方许可和四个平台的二进制。打包只收录这些明确列出的文件。面向源码使用者可再运行 `make source`，它归档当前 Git 提交。

## 开发

`cmd/bupt/` 是程序入口，`internal/` 是 CLI、TUI、控制器和学校平台适配代码，`packaging/` 是随包交付给使用者的文件，`scripts/` 负责构建与打包。

```sh
make build
make test
make race
make vet
```

`make install` 用源码构建并安装本机版本。发布流程和校验方法见[分发说明](docs/releasing.md)，开发接口见[架构](docs/architecture.md)与[协议](docs/protocol.md)。
