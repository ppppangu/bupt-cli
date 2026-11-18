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

课堂演示可以直接打开 `bupt tui api` 或 `bupt tui compute`。方向键选择，Enter 打开，Esc 返回，`q` 退出。编写脚本时可用 `bupt api model --json` 取得结构化结果。实例启停、SSH、文件和后台作业的例子见[使用手册](docs/usage.md)。

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
