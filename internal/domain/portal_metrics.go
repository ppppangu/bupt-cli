package domain

import (
	"encoding/json"
	"sort"
)

// Optional measurements stay nil when absent or null in the school response.
// Zero is a published measurement.
type PortalUsage struct {
	Days        int                `json:"days"`
	Available   *bool              `json:"usageAvailable"`
	Calls       *int64             `json:"periodCalls"`
	Tokens      *int64             `json:"periodTokens"`
	SuccessRate *float64           `json:"successRate"`
	Input       []*int64           `json:"inputSeries"`
	Output      []*int64           `json:"outputSeries"`
	Dates       []string           `json:"dates"`
	Models      []ModelConsumption `json:"modelDistribution"`
}
type ModelConsumption struct {
	Name  string `json:"name"`
	Value *int64 `json:"value"`
}
type PortalLeaderboard struct {
	Days     int         `json:"windowDays"`
	Models   []ModelRank `json:"modelLeaderboard"`
	Users    []UserRank  `json:"userLeaderboard"`
	Me       *OwnRank    `json:"me"`
	Unlocked bool        `json:"disclosureUnlocked"`
}
type ModelRank struct {
	Rank  int      `json:"rank"`
	Model string   `json:"model"`
	Share *float64 `json:"sharePercent"`
}
type UserRank struct {
	Rank   int    `json:"rank"`
	Label  string `json:"label"`
	Tokens *int64 `json:"totalTokens"`
}
type OwnRank struct {
	Ranked bool   `json:"ranked"`
	Rank   int    `json:"rank"`
	Tokens *int64 `json:"totalTokens"`
	Reason string `json:"reason"`
}
type LeaderboardPreference struct {
	Mode     string `json:"mode"`
	Unlocked bool   `json:"unlocked"`
	Options  map[string]struct {
		Available bool   `json:"available"`
		Preview   string `json:"preview"`
	} `json:"options"`
}

func (b *PortalLeaderboard) UnmarshalJSON(raw []byte) error {
	type wire PortalLeaderboard
	var v wire
	if e := json.Unmarshal(raw, &v); e != nil {
		return e
	}
	if v.Models == nil || v.Users == nil {
		return E("PORTAL_PROTOCOL", "榜单缺少模型或用户列表。")
	}
	if m := v.Me; m != nil && (m.Ranked && m.Rank < 1 || m.Tokens != nil && *m.Tokens < 0) {
		return E("PORTAL_PROTOCOL", "个人排名字段无效。")
	}
	for _, r := range v.Models {
		if r.Rank <= 0 || r.Model == "" || r.Share != nil && (*r.Share < 0 || *r.Share > 100) {
			return E("PORTAL_PROTOCOL", "模型排行字段无效。")
		}
	}
	for _, r := range v.Users {
		if r.Rank <= 0 || r.Label == "" || r.Tokens != nil && *r.Tokens < 0 {
			return E("PORTAL_PROTOCOL", "用户排行字段无效。")
		}
	}
	sort.SliceStable(v.Models, func(i, j int) bool { return v.Models[i].Rank < v.Models[j].Rank })
	sort.SliceStable(v.Users, func(i, j int) bool { return v.Users[i].Rank < v.Users[j].Rank })
	*b = PortalLeaderboard(v)
	return nil
}
func (p *LeaderboardPreference) UnmarshalJSON(raw []byte) error {
	type wire LeaderboardPreference
	var v wire
	if e := json.Unmarshal(raw, &v); e != nil {
		return e
	}
	switch v.Mode {
	case "anonymous", "partial", "real":
	default:
		return E("PORTAL_PROTOCOL", "门户未返回有效的榜单展示方式。")
	}
	*p = LeaderboardPreference(v)
	return nil
}

func (u *PortalUsage) UnmarshalJSON(raw []byte) error {
	type wire PortalUsage
	var v wire
	if e := json.Unmarshal(raw, &v); e != nil {
		return e
	}
	if v.Available == nil && v.Calls == nil && v.Tokens == nil && v.SuccessRate == nil && v.Input == nil && v.Output == nil && v.Models == nil {
		return E("PORTAL_PROTOCOL", "门户未返回用量数据或可用状态。")
	}
	if v.Calls != nil && *v.Calls < 0 || v.Tokens != nil && *v.Tokens < 0 || v.SuccessRate != nil && (*v.SuccessRate < 0 || *v.SuccessRate > 100) {
		return E("PORTAL_PROTOCOL", "门户用量汇总字段无效。")
	}
	for _, series := range [][]*int64{v.Input, v.Output} {
		for _, n := range series {
			if n != nil && *n < 0 {
				return E("PORTAL_PROTOCOL", "每日 Token 数量无效。")
			}
		}
	}
	for _, m := range v.Models {
		if m.Name == "" || m.Value != nil && *m.Value < 0 {
			return E("PORTAL_PROTOCOL", "模型用量字段无效。")
		}
	}
	*u = PortalUsage(v)
	return nil
}
