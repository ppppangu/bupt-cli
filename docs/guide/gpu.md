---
description: "核对学校日价，申请一台有运行时限的实例，验证 SSH 并归还算力。"
---

# 申请 GPU 并连接 SSH

[登录](README.md)后运行 `bupt tui compute`。在「资源与价格」中查看可用规格和个人配额，再返回「裸算力服务器」。

## 申请并连接

1. 选择「创建实例」和所需模板。给实例起一个容易辨认的名字，例如 `lab`。
2. 填写运行上限，核对 GPU 数量、学校估算的积分日价和到期处理。选择创建并启动。确认提交会向学校申请资源，排队和启动时间也计入运行上限。
3. 在本次申请的详情页查看进度。需要审批或正在排队时，保存操作 ID，下次可用 `bupt compute instance wait --operation 操作ID` 续等。
4. SSH 就绪后，在详情页选择「连接 SSH」。在远端运行 `hostname` 查看主机名，运行 `exit` 回到实例详情。

在另一个本地终端也可以连接。下面的 `lab` 是上一步设置的实例名，遇到重名时使用实例 ID。

```sh
bupt compute instance wait lab
bupt ssh lab
```

如果只看到平台状态“运行中”，继续等待 SSH 就绪。若提示缺少实例终端密码，在交互终端运行 `bupt auth access`，按提示输入**实例终端密码**，然后续等。

## 用完后归还算力

退出 SSH 只会关闭连接。还想保留算力预留时可以停机。若实验已经完成，要归还 GPU，先确认文件已保存，再在**本地终端**依次执行。将 `lab` 换成自己的实例名。

```sh
bupt compute instance stop lab --wait
bupt compute instance release lab --wait
bupt compute instance show lab
```

在详情中核对实例已停机、计算资源已归还。预留和存储分别计费，磁盘按学校回收策略保留。到期清理若显示 `cleanup_pending`，仍需检查最终状态。[查看账单](usage.md) · [上传文件并运行后台作业](jobs.md)
