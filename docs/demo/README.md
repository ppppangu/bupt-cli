# README 演示

每个动画用 [VHS](https://github.com/charmbracelet/vhs) 驱动正常编译的 `cmd/bupt`。录制使用私有 Unix Socket、实际控制器、模拟学校平台和回环 SSH 服务；模型、价格、问题及回答均为演示数据。

## 重录

安装 Go 1.26、VHS、ttyd、FFmpeg、jq 和 Chrome 或 Chromium。终端字体优先用 Noto Sans Mono CJK SC。四段均以 100 列、28 行和 12 fps 录制。

```sh
./docs/demo/record.sh
./docs/demo/record.sh tui-compute
./docs/demo/record.sh tui-model tui-integration cli-demo
```

脚本可分别运行任一 tape，同一仓库内无需事先安装 `bupt` 或登录。每次使用新建的临时配置、控制器和密钥，录制完成后由测试框架关闭并清理。所有断言通过后，才原子替换对应 GIF。

## 验证

| 工作流 | 状态与结果断言 |
| --- | --- |
| 创建并连接 GPU | 按真实键盘流程核价、创建、等待、SSH、退出和停止。校验实际 SSH 收到了 `hostname` 和 `exit`，创建、启动、停机各执行一次，停机保留分配。 |
| 在线调试 | 验证选中的模型、Key 及请求正文，且模拟账本只增加一次调用，终端显示回答和用量。 |
| 程序接入 | 页面显示终端复制反馈。分别导出并检查 Python 和 curl 的 Base URL、模型 ID 和 `BUPT_API_KEY` 引用，确认示例无密钥且不触发推理。 |
| CLI | 跑真实的模型查询与示例命令，再执行创建、等待、SSH 和停机，核对模拟平台和 SSH 服务实际收到的操作。 |

数据与断言位于 [readme_demo_test.go](../../internal/control/readme_demo_test.go)，共用 [真实终端验收夹具](../../internal/control/tui_session_test.go)。普通 `go test ./...` 不依赖浏览器或 VHS。
