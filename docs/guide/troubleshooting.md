---
description: "按卡住的地方对症查：安装、登录、实例连接，或任务中断后怎么恢复。"
---

# 常见问题

先看卡在哪一步：打开工作台时看本机控制器，扫码时看认证阶段，任务已经提交过的话就去找对应的回执。

## 安装后找不到 bupt

开个新终端运行 `bupt version`。还是提示找不到命令，试试 `~/.local/bin/bupt version`——能看到版本号就说明装好了，直接用 `~/.local/bin/bupt tui` 打开工作台。自定义了安装目录的话，把路径换成实际的。

## 提示 AGENT_NOT_RUNNING

这里的 agent 指 bupt 的本机控制器。打开 `bupt tui`，首页选「首次设置这台电脑」或「启动本机控制器」。Linux 或 WSL2 没有用户级 systemd 的话，选「作为本机进程启动」。然后用 `bupt agent status` 确认连上了。

## 登录卡在二维码

先看提示是 WebVPN 还是统一身份认证，每一步都需要在手机上确认。码过期了就重新运行 `bupt login`，AI agent 用 `bupt login --agent`。学校要求替换另一个 WebVPN 会话时，这个决定交给账号本人。[Agent 扫码方式](agents.md)

## 实例显示运行中，SSH 仍连不上

1. 用 `bupt compute instance show lab` 核对实例状态，把 `lab` 换成你自己的实例名或 ID。
2. 还在准备 SSH 的话，运行 `bupt compute instance wait lab` 等连接验证完成；提示要实例终端密码，就运行 `bupt auth access` 补上凭据。
3. 实例已经在跑、但提示缺 SSH 路由，学校会话有效的情况下运行 `bupt compute instance prepare-ssh lab`，再用 `bupt ssh lab` 连接。校外连接还需要一个能直连的地址，或者配好的中继。

## 终端断开，任务还在吗？

一次实验通常对应三种 ID：实例名或实例 ID 认机器，操作 ID 跟踪一次创建、停止或释放，作业 ID 跟踪一次程序运行。同一台机器可能被操作好几次，续等的时候要用当时那次提交返回的 ID，不能随便拿一个顶替。

提交实例操作后拿到的操作 ID，可以接着跑 `bupt compute instance wait --operation 操作ID`。后台作业用 `bupt compute job show JOB_ID` 看当前状态，再用 `bupt compute job wait JOB_ID` 续等；把命令里的 ID 换成各自的实际值。要是清理状态一直停在 `cleanup_pending`，查一下登录状态和实例的预留情况。

拿到的返回不一样，下一步也不一样：

| 收到什么 | 下一步 |
| --- | --- |
| 实例操作已提交并返回操作 ID | 用 `bupt compute instance wait --operation 操作ID` 续等 |
| 前台作业等待超时，JSON 错误里带 `run_id` | 拿这个 ID 执行 `bupt compute job wait JOB_ID --json` |
| 提交响应丢了，报 `SUBMISSION_UNKNOWN` | 保存返回的 `idempotency_key`，网络恢复后用相同参数、相同费用授权加上 `--idempotency-key` 重试 |
| 模型调用报 `MODEL_RESULT_UNKNOWN` | 先用 `bupt api usage` 核对最近用量，再自己决定要不要重新发送 |
| 参数错了，退出码 2、错误是 `USAGE` | 查对应命令的帮助，改参数重试 |

脚本要分别读 stdout 的结果、stderr 的诊断信息和进程退出码。如果作业需要新申请算力，先用 `--dry-run` 估价，自己确认好日价上限和运行时长，再正式提交。更完整的脚本示例见[这里](https://github.com/ppppangu/bupt-cli/blob/main/docs/usage.md#用脚本提交作业)。

## 反馈问题

记下出错的命令、发生时间和错误码。运行 `bupt feedback --template` 能生成反馈正文，`bupt doctor --report` 能生成脱敏诊断。工作台的「帮助与反馈」里也能找到联系方式。发送前记得去掉密码、完整 API Key、令牌和私钥。
