package control

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
)

// Build the same personal quota request as the school's quota dialog. The
// quota entity itself, including its owner and domain, comes from the snapshot.
func quotaRequest(s domain.Snapshot, p Params) (domain.TaskRequest, error) {
	id := p.ID("target")
	t := domain.TaskRequest{UserID: s.Identity.UID, TargetID: id}
	if err := validateNumericResourceID(id, "quota", false); err != nil {
		return t, err
	}
	var row map[string]any
	typ := ""
	for _, prefix := range []string{"UserQuota", "UserStorageQuota"} {
		if r, ok := s.Quota[prefix+":"+string(id)].(map[string]any); ok {
			row = r
			typ = prefix
			break
		}
	}
	if row == nil {
		return t, domain.E("NOT_FOUND", "当前账号快照没有这个额度 ID。")
	}
	owner := row["userId"]
	if owner == nil {
		owner = row["uid"]
	}
	if owner == nil || fmt.Sprint(owner) != string(s.Identity.UID) {
		return t, domain.E("OWNERSHIP_UNVERIFIED", "额度未绑定到当前已验证的账号。")
	}
	reason := strings.TrimSpace(p.String("reason"))
	if reason == "" {
		return t, domain.E("REASON_REQUIRED", "请说明申请额度的用途。")
	}
	data := map[string]any{}
	requestType := "UpdateUserQuota"
	for _, key := range []string{"cpu", "gpu", "memory", "appCount", "blockStorage", "fileStorage"} {
		val := p.String(key)
		if val == "" {
			continue
		}
		dest := key
		if typ == "UserStorageQuota" {
			if key != "blockStorage" && key != "fileStorage" {
				return t, domain.E("INVALID_RESOURCE", "存储额度只接受磁盘或文件空间。")
			}
			dest = key + "QuotaInMb"
		}
		if _, ok := row[dest]; !ok {
			return t, domain.E("UNSUPPORTED_RESOURCE", "学校未在此额度上发布 "+dest)
		}
		switch key {
		case "memory", "blockStorage", "fileStorage":
			kind := "disk"
			if key == "memory" {
				kind = "memory"
			}
			v, e := resourceValue(kind, val)
			if e != nil {
				return t, e
			}
			data[dest] = json.Number(v)
		case "gpu":
			n, ok := new(big.Rat).SetString(val)
			if !ok || n.Sign() < 0 {
				return t, domain.E("INVALID_RESOURCE", "请提供 GPU 卡数。")
			}
			n.Mul(n, big.NewRat(100, 1))
			if !n.IsInt() || !n.Num().IsInt64() {
				return t, domain.E("INVALID_RESOURCE", "GPU 精度为 0.01 卡。")
			}
			data[dest] = n.Num().Int64()
		default:
			n, e := strconv.ParseInt(val, 10, 64)
			if e != nil || n < 0 {
				return t, domain.E("INVALID_RESOURCE", "请提供非负整数额度。")
			}
			data[dest] = n
		}
	}
	if len(data) == 0 {
		return t, domain.E("INPUT_REQUIRED", "请指定至少一种资源额度。")
	}
	if typ == "UserStorageQuota" {
		requestType = "UpdateUserStorageQuota"
		data["uid"] = s.Identity.UID.Number()
		data["storageName"] = row["storageName"]
	} else {
		data["userId"] = s.Identity.UID.Number()
		data["domain"] = row["domain"]
		if data["domain"] == nil {
			data["domain"] = ""
		}
	}
	for _, req := range s.Renewals {
		if req.Type == requestType && req.TargetID == id && (req.State == "pending" || req.State == "created") {
			return t, domain.E("REQUEST_PENDING", "相同额度已有待审批申请。")
		}
	}
	t.Type = "CreateUserOpRequestTask"
	t.Payload = map[string]any{"tgtEid": id.Number(), "type": requestType, "reason": reason, "data": string(domain.Result(data))}
	return t, nil
}
