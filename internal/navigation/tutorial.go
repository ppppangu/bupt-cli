package navigation

// Tutorial topics are shared by the offline CLI help and the TUI.
type Lesson struct {
	ID, Title, Text string
}

var Lessons = []Lesson{
	{"start", "首次使用", "打开 bupt tui。首次在本机使用时，从首页初始化个人工作区。选择登录学校账号，先完成 WebVPN 扫码，再按提示完成统一身份认证。\n\n想问模型，选「大模型 API 服务 → 试用模型」。想用 GPU 跑自己的程序，选「裸算力服务」。\n\n「账号状态与配置 → 顶栏显示」可调整余额、姓名等字段。终端命令 bupt init 和 bupt login 也可完成初始化与登录。"},
	{"keys", "TUI 操作", "↑ ↓ 选择操作，→ 进入详情或展开分组。按 Enter 执行选中项，底栏会提示它的作用。← 回到上一级，Esc 返回上一页。阅读正文时用 ↑ ↓ 滚动，PgUp/PgDn 翻页。\n\n单行字段用 ← → 移动光标，Enter 保存。模型的多行输入用 Enter 换行，在末行按 ↓ 选「完成编辑」，按 Enter 回表单。Esc 保留本次草稿并返回。\n\n/ 开始筛选，Enter 确认筛选。? 查看本页帮助和对应命令，r 刷新，q 退出。"},
	{"api", "模型试用与接入", "在「试用模型」中先选用途。可以提问、把文本转为向量，或按问题给候选段落排序。填写输入，准备好当前模型的 Key 后选择「发送一次」。学校按实际调用计量。\n\n需要把调用写进自己的程序时，打开「接入示例」。选模型和示例格式，按需编辑输入，照「准备调用密钥」配置运行环境。「公网地址（Base URL）」可单独复制。\n\n在代码正文按 Enter 或 c 复制全文，s 另存。用「余额与用量」可选近 7 天或 30 天的统计。"},
	{"compute", "算力资源生命周期", "在「裸算力服务」选择创建实例，然后选择模板。确认所需 GPU、CPU 和内存，填写名称和运行时限后预览申请。\n\n提交后查看实例准备进度。SSH 就绪时，进入实例详情选择「连接 SSH」。用完后查看停止操作对磁盘和预留额度的影响，再确认释放。\n\n命令行可用 bupt compute catalog 查看资源目录，bupt compute quota 查看额度，bupt compute instance 查看自己的实例。"},
	{"cli", "CLI 入门", "bupt 打印命令索引。bupt help api 和 bupt help compute 列出业务命令。用 bupt help api model try --all 查看指定命令的完整选项。\n\n脚本可用 bupt api model --json 取得结构化结果；支持 -q 的列表查询会逐行输出标识。bupt help --all 提供完整命令树。"},
	{"trouble", "常见问题", "学校提示网络会话过期时运行 bupt login，在当前网络重新认证。用 bupt auth status 查看学校身份。遇到权限不足时核对账号角色和功能开关。\n\n模型试用找不到 Key 时，检查它已启用、允许当前模型并由所选控制器保管。实例和托管作业状态可用 bupt status 查看。"},
}
