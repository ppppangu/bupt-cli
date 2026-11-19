# 演示素材

仓库根目录的三个 GIF 由 `assets/demo/tapes/*.tape` 用 [vhs](https://github.com/charmbracelet/vhs) 渲染。
动图展示的是真实 TUI 的画面与真实 CLI 帮助文本，数据为**脚本化的模拟内容**，不涉及任何真实账号。

- `bupt-tui-api.gif` — TUI 模型 API：查模型 → 在线调试 → 选密钥 → 发送并查看回复
- `bupt-tui-compute.gif` — TUI 裸算力：资源目录 → 创建实例表单 → 实例筛选与详情
- `bupt-cli.gif` — CLI：帮助、`--json` 管道 jq、实例与用量查询

## 重新生成

依赖：Go 1.26、Python 3、vhs、ffmpeg、jq（CLI 演示用到）。

```sh
# 1. 从真实 TUI 代码渲染逐键帧（模拟数据）
go run -tags demogen ./scripts/demogen assets/demo

# 2. 录制动图
vhs assets/demo/tapes/api.tape -o assets/bupt-tui-api.gif
vhs assets/demo/tapes/compute.tape -o assets/bupt-tui-compute.gif
vhs assets/demo/tapes/cli.tape -o assets/bupt-cli.gif
```

原理：`scripts/demogen` 用构建标签 `demogen` 编译进 `internal/tui`，把每帧按键对应的
真实渲染画面写成 ANSI 文件（`assets/demo/api/`、`assets/demo/compute/`）。录制时
`assets/demo/bin/bupt` 是一个演示 shim，TUI 命令交给 `play.py` 按录制按键逐帧播放，
CLI 命令输出 `assets/demo/cli/` 里的模拟数据；帮助文本取自真实二进制的输出。
所有按键与帧一一对应，改动旅程后需同步调整对应 `.tape` 的按键序列。

真实账号永远不会被触碰。演示数据（模型、密钥、实例）全部虚构。
