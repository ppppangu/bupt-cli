package navigation

// Tutorial topics are shared by the offline CLI help and the TUI.
type Lesson struct {
	ID, Title, Text string
}

var Lessons = []Lesson{
	{"start", "首次使用", "从首页选「首次设置这台电脑」，启动个人工作区后登录学校账号。校外认证过期时先扫 WebVPN 二维码，再按提示完成学校登录。\n\n想提问或在 Python 里调用模型，选「模型 API」。想用学校的 CPU 或 GPU 运行自己的代码，选「裸算力服务器」。\n\n「账户及相关设置」可查看登录状态和公告。终端命令 bupt init 和 bupt login 也可完成首次设置。"},
	{"keys", "快捷键说明", "首页按 R 进入模型 API，S 进入裸算力服务器，U 查看余额与用量，? 打开帮助与账户。资源页按 ? 查看本页帮助。\n\n↑↓ 或 j/k 选择，Enter 执行，Tab / Shift+Tab 换控件。/ 筛选，r 刷新。选择器按 Enter 应用，Esc 取消。密钥和实例列表按 n 新建，授权模型用 Space 勾选。\n\n编辑多行文本时 Enter 换行，Esc 保留草稿。回到调试工作台，选「发送请求」才会提交。q 退出，Ctrl+C 中断前台等待或退出。"},
	{"api", "API 使用文档", "在「模型 API → 在线调试」选「对话」和模型，编辑问题，Esc 保留草稿。选择调用密钥或为此模型创建密钥，再选「发送请求」。回复显示在原页，可继续编辑。发送按用量计费。\n\n在调试工作台选「生成调用代码」。在代码工作台选择 Python 或 curl，通过「环境配置与依赖」设置密钥后运行示例。\n\n首页「余额与用量」显示积分、调用次数和 Token 用量，可切换到服务器账单。命令行可用 bupt api usage --days 7 查询模型用量。"},
	{"compute", "申请第一台实例并运行程序", "从「裸算力服务器」选择「创建实例」。选机器规格，填写实例名称、运行上限和所需项目。核对学校日价、申请资源与到期处理后提交。\n\n提交后留在实例详情，学校确认分配后会在原页显示状态。资源分配后启动并准备 SSH，节点就绪后可连接或上传代码。\n\n在已托管的实例选「提交后台作业」。例如运行 python train.py，可执行文件填 python，第一个参数填 train.py。提交后可查看这次作业的输出。\n\n托管截止和平台预留到期可能不同。用完选择「停止并归还算力」可保留实例磁盘。"},
	{"cli", "CLI 入门", "bupt 打印命令索引。bupt help api 和 bupt help compute 列出业务命令。用 bupt help api model invoke --all 查看指定命令的完整选项。\n\n脚本可用 bupt api model --json 取得结构化结果；--output id 逐行输出稳定标识。bupt help --all 提供完整命令树。"},
	{"trouble", "常见问题", "学校提示网络会话过期时运行 bupt login，在当前网络重新认证。用 bupt auth status 查看学校身份。遇到权限不足时核对账号角色和功能开关。\n\n模型调用找不到 Key 时，检查它已启用、允许当前模型并由所选控制器保管。实例和托管作业状态可用 bupt status 查看。"},
}
