// Package navigation defines the sections shared by the command index and the TUI.
package navigation

type Area struct {
	ID, Name, Summary string
	Commands          []string
}

const (
	APITitle     = "模型 API"
	ComputeTitle = "裸算力服务器"
	AccountTitle = "账户及相关设置"
	HelpTitle    = "帮助与反馈"
)

var Areas = []Area{
	{"api", APITitle, "查询模型、余额、用量和管理 API Key", []string{"api"}},
	{"compute", ComputeTitle, "查看实例、节点、连接和使用期限", []string{"compute", "compute instance", "ssh", "exec", "forward"}},
	{"resources", "资源与模板", "查看配额、选择模板、估价和保存预设", []string{"compute catalog", "compute quota", "compute preset"}},
	{"work", "作业与记录", "提交作业、看日志、跟踪运行与平台回执", []string{"compute job", "compute scheduler", "compute task", "compute audit"}},
	{"files", "文件", "浏览持久存储，在本机和实例之间传文件", []string{"compute storage", "cp", "sftp"}},
	{"access", AccountTitle, "管理登录、顶栏、控制器和连接，查看平台公告与协议", []string{"login", "logout", "account", "news", "agreement", "auth", "agent", "profile", "client key", "ssh-config", "init", "config", "client", "proxy", "doctor"}},
}

func Find(id string) (Area, bool) {
	for _, area := range Areas {
		if area.ID == id {
			return area, true
		}
	}
	return Area{}, false
}
