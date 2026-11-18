package navigation

// Tutorial topics are shared by the offline CLI help and the TUI.
type Lesson struct {
	ID, Title, Text string
}

var Lessons = []Lesson{
	{"start", "首次使用", "先运行 bupt init 启动个人控制器，再运行 bupt login 完成学校认证。用 bupt status 查看当前账号。\n\n在 bupt tui 首页选择大模型 API 服务或裸算力服务。在「账号状态与配置 → 顶栏显示」选择姓名、学工号、余额或运行实例数，或手动核验登录状态。"},
	{"keys", "TUI 操作", "↑ ↓ 或 j/k 选择一行，Enter 打开，Esc 返回，/ 开始筛选，编辑筛选时 Ctrl+U 清空，Enter 确认，PgUp/PgDn 翻页，r 刷新当前页面，? 查看本页帮助，q 退出。\n\n在模型输入框中，Enter 换行，Ctrl+S 保存，试用编辑器内 Ctrl+D 发送一次，Esc 取消本次编辑。"},
	{"api", "模型试用与接入", "用 bupt api model 查看模型 ID 和能力。\n\n在大模型 API 服务中打开「接入示例」，选模型、格式和输入，按 Enter 展开全文，Enter 或 c 复制。脚本从 BUPT_API_KEY 读取完整密钥。\n\n在「试用模型」中输入问题、要向量化的文本，或检索问题与候选段落。选择已授权的 Key 后发送一次，按学校实际调用计量。bupt api usage 可查用量。"},
	{"compute", "算力资源生命周期", "bupt compute catalog 查看资源目录，bupt compute quota 查看额度，bupt compute instance 查看自己的实例。\n\n打开实例详情，确认规格、预留状态和期限。启动前检查预计消耗与清理策略；用完后检查实例是否已停止、预留是否释放。磁盘和永久删除的影响以操作预览为准。"},
	{"cli", "CLI 入门", "bupt 打印命令索引。bupt help api 和 bupt help compute 列出业务命令。用 bupt help api model try --all 查看指定命令的完整选项。\n\n脚本可用 bupt api model --json 取得结构化结果；支持 -q 的列表查询会逐行输出标识。bupt help --all 提供完整命令树。"},
	{"trouble", "常见问题", "学校提示网络会话过期时运行 bupt login，在当前网络重新认证。用 bupt auth status 查看学校身份。遇到权限不足时核对账号角色和功能开关。\n\n模型试用找不到 Key 时，检查它已启用、允许当前模型并由所选控制器保管。实例和托管作业状态可用 bupt status 查看。"},
}
