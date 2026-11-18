package platform

import (
	"sort"
	"strings"

	"bupt.local/cli/internal/domain"
)

// Explicit entity-field lists keep credential hashes, registration tokens and
// arbitrary server payloads out of snapshots and CLI JSON output.
var entityFields = map[string]string{
	"App":                      "eid name specEid userId groupId domain eaasIndex state healthState hasReserved reserveType expStopAt expDuration createdAt reservedAtOrQueueAt operateTaskId meta executors storageName",
	"ResourceActorControlInfo": "eid meta",
	"Executor":                 "eid name appEid state clusters services",
	"Cluster":                  "eid name appEid executorEid nodes",
	"Node":                     "eid name uname uid gid clusterEid appEid type role state eaasIndex resourceAlloc services ports storages snapshots meta imageName imageType",
	"ExecutorService":          "eid name type uiType state healthState nodeEid nodeName clusterEid executorEid exposeOnHostNode exposeOnGw exposeOnUi meta ports",
	"PortMap":                  "eid serviceEid label innerBind hostBind port hostPort gwIp gwPort gwEid",
	"User":                     "eid uid uname displayName",
	"UserPrjUserGroupRole":     "eid uid gid role state",
	"Task":                     "eid id type state code createdAt updatedAt eaasIndex",
	"UserOpRequest":            "eid uid type state tgtEid data createdAt updatedAt",
	"UserQuota":                "eid uid userId domain cpu gpu memory appCount cpuUsed gpuUsed memoryUsed appCountUsed usedCpu usedGpu usedMemory usedAppCount blockStorage fileStorage blockStorageUsed fileStorageUsed",
	"UserStorageQuota":         "eid uid storageName blockStorageQuotaInMb blockStorageUsedInMb fileStorageQuotaInMb fileStorageUsedInMb",
	"Storage":                  "eid name type size sizeInMb mountPath remotePath storageEid storageName capacity meta readWrite hostPath nodePath contentType privilege",
	"ResourceAlloc":            "eid arch bindCpu cpu memory gpu gpuMem gpuType fpga accelDisk",
	"NodeStorage":              "eid name type size sizeInMb mountPath remotePath storageEid storageName capacity meta",
	"Snapshot":                 "eid name desc nodeEid state createdAt size sizeInMb",
	"Meta":                     "eid key value",
}

func sensitive(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"password", "secret", "token", "cookie", "privatekey", "private_key", "authorization"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}
func cleanValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cleanMap(x)
	case []any:
		a := make([]any, 0, len(x))
		for _, v := range x {
			a = append(a, cleanValue(v))
		}
		return a
	default:
		return v
	}
}
func cleanMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	if sensitive(stringValue(m["key"])) {
		return nil
	}
	out := map[string]any{}
	for k, v := range m {
		if !sensitive(k) {
			out[k] = cleanValue(v)
		}
	}
	return out
}
func pick(m map[string]any, fields ...string) map[string]any {
	out := map[string]any{}
	for _, k := range fields {
		if v, ok := m[k]; ok && !sensitive(k) {
			out[k] = cleanValue(v)
		}
	}
	return out
}
func filterEntity(typ string, m map[string]any) map[string]any {
	fields, ok := entityFields[typ]
	if !ok {
		return nil
	}
	if typ == "Meta" && sensitive(stringValue(m["key"])) {
		return nil
	}
	return pick(m, strings.Fields(fields)...)
}
func fieldsMap(v any) map[string]any {
	m := map[string]any{}
	for _, f := range array(v) {
		o := object(f)
		m[stringValue(o["name"])] = o["value"]
	}
	return m
}
func (v *view) apply(update map[string]any) error {
	for _, raw := range array(update["operations"]) {
		o := object(raw)
		typ := stringValue(o["typeName"])
		eid := id(o["eid"])
		switch stringValue(o["operName"]) {
		case "entityAdd", "entityUpdate":
			if _, ok := entityFields[typ]; !ok {
				continue
			}
			if v.Entities[typ] == nil {
				v.Entities[typ] = map[domain.ID]map[string]any{}
			}
			m := v.Entities[typ][eid]
			if stringValue(o["operName"]) == "entityAdd" {
				m = nil
			}
			if m == nil {
				m = map[string]any{"eid": eid}
			}
			for k, value := range fieldsMap(o["fields"]) {
				m[k] = value
			}
			m = filterEntity(typ, m)
			if m != nil {
				v.Entities[typ][eid] = m
			}
			for _, key := range []string{"meta", "addMeta", "updateMeta"} {
				for _, rawMeta := range array(o[key]) {
					meta := filterEntity("Meta", object(rawMeta))
					if meta == nil {
						continue
					}
					mid := id(meta["eid"])
					if v.Entities["Meta"] == nil {
						v.Entities["Meta"] = map[domain.ID]map[string]any{}
					}
					v.Entities["Meta"][mid] = meta
					v.addRelation(relation{Label: "meta", Source: eid, Target: mid})
				}
			}
			for _, mid := range array(o["delMeta"]) {
				v.removeEntity("Meta", id(mid))
			}
		case "entityDelete":
			v.removeEntity(typ, eid)
		case "relationAdd":
			v.addRelation(relation{Label: stringValue(o["label"]), Source: id(o["srcEid"]), Target: id(o["tgtEid"])})
		case "relationDel":
			r := relation{Label: stringValue(o["label"]), Source: id(o["srcEid"]), Target: id(o["tgtEid"])}
			rels := v.Relations[:0]
			for _, old := range v.Relations {
				if old != r {
					rels = append(rels, old)
				}
			}
			v.Relations = rels
		default:
			return domain.E("PROTOCOL", "unknown memview operation; full synchronization required")
		}
	}
	return nil
}
func (v *view) addRelation(r relation) {
	for _, old := range v.Relations {
		if old == r {
			return
		}
	}
	v.Relations = append(v.Relations, r)
}
func (v *view) removeEntity(typ string, eid domain.ID) {
	delete(v.Entities[typ], eid)
	rels := v.Relations[:0]
	for _, r := range v.Relations {
		if r.Source != eid && r.Target != eid {
			rels = append(rels, r)
		}
	}
	v.Relations = rels
}

type graph struct {
	entities  map[string]map[domain.ID]map[string]any
	relations []relation
}

func (s *eventSocket) graph() graph {
	g := graph{entities: map[string]map[domain.ID]map[string]any{}}
	for _, v := range s.views {
		for typ, records := range v.Entities {
			if g.entities[typ] == nil {
				g.entities[typ] = map[domain.ID]map[string]any{}
			}
			for eid, record := range records {
				g.entities[typ][eid] = record
			}
		}
		g.relations = append(g.relations, v.Relations...)
	}
	return g
}
func (g graph) children(eid domain.ID, labels ...string) []domain.ID {
	ids := []domain.ID{}
	for _, r := range g.relations {
		if r.Source == eid {
			for _, label := range labels {
				if r.Label == label {
					ids = append(ids, r.Target)
				}
			}
		}
	}
	return ids
}
func (g graph) meta(eid domain.ID, inline any) map[string]any {
	m := cleanMap(object(inline))
	if m == nil {
		m = map[string]any{}
	}
	for _, mid := range g.children(eid, "meta", "__meta__") {
		if r := g.entities["Meta"][mid]; r != nil {
			key := stringValue(r["key"])
			if !sensitive(key) {
				m[key] = r["value"]
			}
		}
	}
	return m
}
func (g graph) descendants(app map[string]any) map[domain.ID]bool {
	ids := map[domain.ID]bool{id(app["eid"]): true}
	queue := []domain.ID{id(app["eid"])}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range g.children(cur, "executors", "clusters", "nodes", "services") {
			if !ids[child] {
				ids[child] = true
				queue = append(queue, child)
			}
		}
	}
	return ids
}
func (g graph) service(m map[string]any) domain.Service {
	sid := id(m["eid"])
	name := stringValue(m["name"])
	health := stringValue(m["healthState"])
	s := domain.Service{ID: sid, Name: name, Type: stringValue(m["type"]), UIType: stringValue(m["uiType"]), Healthy: health == "healthy" || health == "success" || health == "ok", Builtin: strings.HasPrefix(name, "_")}
	switch name {
	case "ctrl", "console", "desktop", "jupyter", "slurm", "interface", "jupyterhub", "ssh":
		s.Builtin = true
	}
	if s.UIType == "" {
		if name == "console" {
			s.UIType = "console"
		} else if name == "ssh" {
			s.UIType = "ssh"
		}
	}
	portIDs := g.children(sid, "ports", "portMaps")
	for pid, p := range g.entities["PortMap"] {
		match := id(p["serviceEid"]) == sid
		for _, target := range portIDs {
			if pid == target {
				match = true
			}
		}
		if match {
			s.Host = stringValue(p["gwIp"])
			s.Port = int(integer(p["gwPort"]))
			if s.Host == "" || s.Port == 0 {
				s.Host = stringValue(p["hostBind"])
				s.Port = int(integer(p["hostPort"]))
			}
			s.InnerHost = stringValue(p["innerBind"])
			s.InnerPort = int(integer(p["port"]))
			break
		}
	}
	return s
}
func (g graph) instance(m map[string]any) domain.Instance {
	eid := id(m["eid"])
	a := domain.Instance{ID: eid, Name: stringValue(m["name"]), OwnerID: id(m["userId"]), UserID: id(m["userId"]), GroupID: id(m["groupId"]), SpecID: id(m["specEid"]), Domain: stringValue(m["domain"]), Index: id(m["eaasIndex"]), State: stringValue(m["state"]), Health: stringValue(m["healthState"]), Reserved: boolValue(m["hasReserved"]), ReserveType: stringValue(m["reserveType"]), ExpiresAt: timestamp(m["expStopAt"]), DurationSeconds: integer(m["expDuration"]), CreatedAt: timestamp(m["createdAt"])}
	a.OperateTaskID = stringValue(m["operateTaskId"])
	a.Meta = map[string]any{}
	if value, ok := m["reservedAtOrQueueAt"]; ok {
		a.Meta["reservedAtOrQueueAt"] = value
	}
	meta := g.meta(eid, m["meta"])
	if value, ok := meta["AutoApproveUpdateAppExpStopAtReq"]; ok {
		a.Meta["AutoApproveUpdateAppExpStopAtReq"] = value
	}
	if len(g.entities["ResourceActorControlInfo"]) == 1 {
		for controlID, control := range g.entities["ResourceActorControlInfo"] {
			if value, ok := g.meta(controlID, control["meta"])["UpdateAppStopAtRequestDeadline"]; ok {
				a.Meta["UpdateAppStopAtRequestDeadline"] = value
			}
		}
	}
	desc := g.descendants(m)
	// Some actor dumps contain foreign-key fields as well as relations. Both are
	// authoritative; never use names to attach a node to an instance.
	for xid, x := range g.entities["Executor"] {
		if id(x["appEid"]) == eid {
			desc[xid] = true
		}
	}
	for cid, cl := range g.entities["Cluster"] {
		if id(cl["appEid"]) == eid || desc[id(cl["executorEid"])] {
			desc[cid] = true
		}
	}
	for nid, n := range g.entities["Node"] {
		if !desc[nid] && !desc[id(n["clusterEid"])] && id(n["appEid"]) != eid {
			continue
		}
		node := domain.Node{ID: nid, Name: stringValue(n["name"]), Username: stringValue(n["uname"]), Type: stringValue(n["type"]), Role: stringValue(n["role"]), State: stringValue(n["state"]), GroupID: id(n["gid"]), Index: id(n["eaasIndex"]), Resources: cleanMap(object(n["resourceAlloc"]))}
		for _, rid := range g.children(nid, "resourceAlloc") {
			if alloc := g.entities["ResourceAlloc"][rid]; alloc != nil {
				node.Resources = cleanMap(alloc)
			}
		}
		for _, sv := range g.entities["ExecutorService"] {
			match := id(sv["nodeEid"]) == nid
			for _, target := range g.children(nid, "services") {
				if target == id(sv["eid"]) {
					match = true
				}
			}
			if match {
				node.Services = append(node.Services, g.service(sv))
			}
		}
		sort.Slice(node.Services, func(i, j int) bool { return node.Services[i].ID < node.Services[j].ID })
		for _, st := range g.children(nid, "storages") {
			for _, typ := range []string{"Storage", "NodeStorage"} {
				if rec := g.entities[typ][st]; rec != nil {
					node.Storages = append(node.Storages, cleanMap(rec))
				}
			}
		}
		for _, ss := range g.children(nid, "snapshots") {
			if rec := g.entities["Snapshot"][ss]; rec != nil {
				node.Snapshots = append(node.Snapshots, cleanMap(rec))
			}
		}
		a.Nodes = append(a.Nodes, node)
	}
	sort.Slice(a.Nodes, func(i, j int) bool { return a.Nodes[i].ID < a.Nodes[j].ID })
	return a
}
func (s *eventSocket) snapshot(identity domain.Identity) domain.Snapshot {
	g := s.graph()
	out := domain.Snapshot{Index: s.index, Identity: identity, Instances: []domain.Instance{}, Quota: map[string]any{}}
	for _, u := range g.entities["User"] {
		if id(u["uid"]) == identity.UID {
			out.Identity.EID = id(u["eid"])
			if out.Identity.Username == "" {
				out.Identity.Username = stringValue(u["uname"])
			}
		}
	}
	out.Identity.Memberships = nil
	seenMemberships := map[string]bool{}
	for _, membership := range g.entities["UserPrjUserGroupRole"] {
		if id(membership["uid"]) != identity.UID {
			continue
		}
		gid := id(membership["gid"])
		if !validIdentityID(gid) {
			continue
		}
		role, ok := membership["role"].(string)
		if !ok {
			continue
		}
		switch role {
		case "admin", "manager", "member":
		default:
			continue
		}
		state, _ := membership["state"].(string)
		key := string(gid) + "\x00" + role + "\x00" + state
		if seenMemberships[key] {
			continue
		}
		seenMemberships[key] = true
		out.Identity.Memberships = append(out.Identity.Memberships, domain.ProjectMembership{GroupID: gid, Role: role, State: state})
	}
	sort.Slice(out.Identity.Memberships, func(i, j int) bool {
		a, b := out.Identity.Memberships[i], out.Identity.Memberships[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.State < b.State
	})
	managed := map[domain.ID]bool{}
	for _, m := range out.Identity.Memberships {
		if (m.Role == "admin" || m.Role == "manager") && (m.State == "" || m.State == "active") {
			managed[m.GroupID] = true
		}
	}
	if len(managed) > 0 {
		out.ProjectMembers = map[string][]domain.ProjectMember{}
		names := map[domain.ID]string{}
		for _, u := range g.entities["User"] {
			names[id(u["uid"])] = stringValue(u["displayName"])
		}
		for _, m := range g.entities["UserPrjUserGroupRole"] {
			gid, uid := id(m["gid"]), id(m["uid"])
			if managed[gid] && validIdentityID(uid) {
				out.ProjectMembers[string(gid)] = append(out.ProjectMembers[string(gid)], domain.ProjectMember{UID: uid, Name: names[uid], Role: stringValue(m["role"]), State: stringValue(m["state"])})
			}
		}
		for g := range out.ProjectMembers {
			members := out.ProjectMembers[g]
			sort.Slice(members, func(i, j int) bool { return members[i].UID < members[j].UID })
		}
	}
	for _, m := range g.entities["App"] {
		out.Instances = append(out.Instances, g.instance(m))
	}
	sort.Slice(out.Instances, func(i, j int) bool { return out.Instances[i].ID < out.Instances[j].ID })
	for _, r := range s.tasks {
		out.Tasks = append(out.Tasks, r)
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].ID < out.Tasks[j].ID })
	for _, m := range g.entities["UserOpRequest"] {
		data := object(m["data"])
		out.Renewals = append(out.Renewals, domain.Renewal{OwnerID: id(m["uid"]), ID: id(m["eid"]), TargetID: id(m["tgtEid"]), Type: stringValue(m["type"]), State: stringValue(m["state"]), RequestedSeconds: integer(data["delaySeconds"]), CreatedAt: timestamp(m["createdAt"])})
	}
	for typ, records := range g.entities {
		if typ == "UserQuota" || typ == "UserStorageQuota" {
			for _, m := range records {
				uid := id(m["uid"])
				if uid == "" {
					uid = id(m["userId"])
				}
				if uid == identity.UID {
					out.Quota[typ+":"+string(id(m["eid"]))] = cleanMap(m)
				}
			}
		}
	}
	return out
}
