---
description: 分清要恢复的对象，判断应继续等待、按原请求重试，还是先查账单。
---

# 用回执恢复任务

一次实验通常有三种标识。**实例名或实例 ID** 找到机器，**操作 ID** 跟踪一次创建、停止或释放，**作业 ID** 跟踪一次程序运行。同一台机器可以有多次操作，所以续等时要用原来那次提交的回执。

## 先提交，再等待

在已就绪的托管实例 `lab` 上，下面的示例会提交一次简短的 Python 作业。先核对实例的剩余时间和清理策略，再执行：

```sh
bupt compute job run --instance lab --detach --output id -- python3 --version
```

保存 stdout 返回的那一行。把它代入 `bupt compute job wait JOB_ID --json`，即可从同一控制器继续等待。用 `bupt compute job logs JOB_ID` 看程序输出。

## 出错后按返回值行动

| 收到什么                           | 下一步                                                               |
| ------------------------------ | ----------------------------------------------------------------- |
| 实例操作已提交并返回操作 ID                | 用 `bupt compute instance wait --operation 操作ID` 续等                |
| 前台作业等待超时，JSON 错误含 `run_id`     | 用该 ID 执行 `bupt compute job wait JOB_ID --json`                    |
| 提交响应丢失，返回 `SUBMISSION_UNKNOWN` | 保存返回的 `idempotency_key`。恢复连接后，使用相同参数、费用授权和 `--idempotency-key` 重试 |
| 模型调用返回 `MODEL_RESULT_UNKNOWN`  | 先用 `bupt api usage` 核查近期用量，再由用户决定是否重新发送                           |
| 参数错误，退出 2 且错误为 `USAGE`         | 查看对应命令的帮助，修正参数                                                    |

脚本分别读取 stdout 的结果、stderr 的诊断和进程退出码。对需要新申请算力的作业，先用 `--dry-run` 估价，由用户授权日价上限和运行时长，再正式提交。[完整脚本示例](https://github.com/ppppangu/bupt-cli/blob/main/docs/usage.md#%E7%94%A8%E8%84%9A%E6%9C%AC%E6%8F%90%E4%BA%A4%E4%BD%9C%E4%B8%9A)
