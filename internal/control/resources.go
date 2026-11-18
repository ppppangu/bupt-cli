package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
)

type resourceField struct {
	Type    string `json:"type"`
	Value   string `json:"value"`
	Options string `json:"options"`
}
type resourceGroup struct {
	Role        string          `json:"role"`
	NodeName    string          `json:"nodeName"`
	Constraints []resourceField `json:"constraints"`
}

var resourceKinds = map[string]string{"cpu": "CPU", "memory": "Memory", "gpu": "GPU", "disk": "AccelDisk", "accelDisk": "AccelDisk", "bindCpu": "BindCPU", "cpuArch": "CPUArch"}
var wireKinds = map[string]string{"CPU": "cpu", "Memory": "memory", "GPU": "gpu", "AccelDisk": "accelDisk", "BindCPU": "bindCpu", "CPUArch": "cpuArch"}
var sizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(B|KiB|MiB|GiB|TiB|KB|MB|GB|TB)?$`)
var decimalResource = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

// The platform labels these units MB but uses 1024-based MiB counts. Bare
// numbers are platform counts; explicit SI byte sizes must be exactly representable.
func resourceValue(kind string, v any) (string, error) {
	text := strings.TrimSpace(fmt.Sprint(v))
	switch kind {
	case "memory", "disk", "accelDisk":
		m := sizePattern.FindStringSubmatch(text)
		if m == nil {
			return "", domain.E("INVALID_RESOURCE", "memory/disk must be platform MiB or a size such as 6GiB")
		}
		n, ok := new(big.Rat).SetString(m[1])
		if !ok {
			return "", domain.E("INVALID_RESOURCE", "invalid resource size")
		}
		factors := map[string]*big.Rat{"": big.NewRat(1, 1), "B": big.NewRat(1, 1<<20), "KiB": big.NewRat(1, 1024), "MiB": big.NewRat(1, 1), "GiB": big.NewRat(1024, 1), "TiB": big.NewRat(1<<20, 1), "KB": big.NewRat(1000, 1<<20), "MB": big.NewRat(1000000, 1<<20), "GB": big.NewRat(1000000000, 1<<20), "TB": big.NewRat(1000000000000, 1<<20)}
		n.Mul(n, factors[m[2]])
		if !n.IsInt() || !n.Num().IsInt64() || n.Sign() < 0 || (n.Sign() == 0 && kind == "memory") {
			return "", domain.E("INVALID_RESOURCE", "size must be a whole platform MiB count; use MiB or GiB to avoid rounding")
		}
		return n.Num().String(), nil
	case "cpu":
		if !decimalResource.MatchString(text) {
			return "", domain.E("INVALID_RESOURCE", "CPU must be a positive decimal number")
		}
		n, ok := new(big.Rat).SetString(text)
		if !ok || n.Sign() <= 0 {
			return "", domain.E("INVALID_RESOURCE", "CPU must be positive")
		}
		return decimalString(n), nil
	case "gpu":
		if text == "" || strings.ContainsAny(text, "\r\n\x00") {
			return "", domain.E("INVALID_RESOURCE", "invalid GPU constraint")
		}
		return text, nil
	case "bindCpu":
		if text != "true" && text != "false" {
			return "", domain.E("INVALID_RESOURCE", "bindCpu must be true or false")
		}
		return text, nil
	case "cpuArch":
		if text == "" || strings.ContainsAny(text, "\r\n\x00") {
			return "", domain.E("INVALID_RESOURCE", "invalid CPU architecture")
		}
		return text, nil
	}
	return "", domain.E("INVALID_RESOURCE", "unknown resource field "+kind)
}
func decimalString(n *big.Rat) string {
	if n.IsInt() {
		return n.Num().String()
	}
	s := n.FloatString(12)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}
func resourceOptions(f resourceField) []string {
	out := []string{}
	for _, v := range strings.Split(f.Options, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func containsResource(vs []string, value string) bool {
	for _, v := range vs {
		if v == value {
			return true
		}
	}
	return false
}
func decodeResourceGroups(spec domain.Spec) (map[string]resourceGroup, error) {
	raw := spec.Options["resourceConstraints"]
	var b []byte
	if s, ok := raw.(string); ok {
		b = []byte(s)
	} else {
		b, _ = json.Marshal(raw)
	}
	groups := map[string]resourceGroup{}
	if len(b) == 0 || string(b) == "null" || string(b) == `""` {
		return groups, nil
	}
	if e := json.Unmarshal(b, &groups); e != nil {
		return nil, domain.E("TEMPLATE_PROTOCOL", "template resource constraints cannot be decoded")
	}
	for id, g := range groups {
		n, e := strconv.ParseUint(id, 10, 64)
		if e != nil || n == 0 {
			return nil, domain.E("TEMPLATE_PROTOCOL", "template node group ID is invalid")
		}
		seen := map[string]bool{}
		for _, f := range g.Constraints {
			if f.Type == "" || seen[f.Type] {
				return nil, domain.E("TEMPLATE_PROTOCOL", "template contains missing or duplicate resource types")
			}
			seen[f.Type] = true
		}
	}
	return groups, nil
}
func normalizeResources(resources map[string]any) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range resources {
		typ := resourceKinds[k]
		if typ == "" {
			return nil, domain.E("INVALID_RESOURCE", "unknown resource "+k)
		}
		if _, ok := out[typ]; ok {
			return nil, domain.E("INVALID_RESOURCE", "resource aliases were specified twice")
		}
		value, e := resourceValue(k, v)
		if e != nil {
			return nil, e
		}
		out[typ] = value
	}
	return out, nil
}
func decodeExactConstraints(exact map[string]any, groups map[string]resourceGroup) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for gid, raw := range exact {
		g, ok := groups[gid]
		if !ok {
			return nil, domain.E("RESOURCE_GROUP_UNKNOWN", "constraint group is not part of the selected template")
		}
		b, e := json.Marshal(raw)
		if e != nil {
			return nil, e
		}
		var entries []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&entries); e != nil || entries == nil {
			return nil, domain.E("INVALID_CONSTRAINTS", "exact constraints must be arrays of {type,value} with string values")
		}
		known := map[string]bool{}
		for _, f := range g.Constraints {
			known[f.Type] = true
		}
		wanted := map[string]string{}
		for _, f := range entries {
			if !known[f.Type] {
				return nil, domain.E("RESOURCE_TYPE_UNKNOWN", "constraint type is not part of its template group")
			}
			if _, exists := wanted[f.Type]; exists {
				return nil, domain.E("INVALID_CONSTRAINTS", "constraint type appears more than once")
			}
			wanted[f.Type] = f.Value
		}
		out[gid] = wanted
	}
	return out, nil
}
func defaultResources(g resourceGroup) map[string]string {
	out := map[string]string{}
	for _, f := range g.Constraints {
		out[f.Type] = f.Value
	}
	return out
}
func groupForNode(groups map[string]resourceGroup, n domain.Node) (string, resourceGroup, error) {
	matches := []string{}
	for id, g := range groups {
		if g.Role == n.Role && g.Role != "" {
			matches = append(matches, id)
		}
	}
	if len(matches) > 1 {
		narrow := []string{}
		for _, id := range matches {
			if groups[id].NodeName != "" && groups[id].NodeName == n.Name {
				narrow = append(narrow, id)
			}
		}
		matches = narrow
	}
	if len(matches) != 1 {
		return "", resourceGroup{}, domain.E("RESOURCE_GROUP_REQUIRED", "node role does not select one template group; ambiguous custom layouts are unsupported")
	}
	return matches[0], groups[matches[0]], nil
}
func scalarResource(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func currentResources(g resourceGroup, node domain.Node) (map[string]string, error) {
	if len(node.Resources) == 0 {
		return nil, domain.E("CURRENT_RESOURCES_REQUIRED", "current node allocation is unavailable; refusing to reset unspecified fields")
	}
	out := defaultResources(g)
	for _, f := range g.Constraints {
		kind := wireKinds[f.Type]
		if kind == "" {
			continue
		}
		raw, ok := node.Resources[kind]
		if kind == "cpuArch" && !ok {
			raw, ok = node.Resources["arch"]
		}
		if !ok {
			switch kind {
			case "gpu", "accelDisk":
				raw = 0
			case "bindCpu":
				raw = false
			case "cpuArch":
				continue
			default:
				return nil, domain.E("CURRENT_RESOURCES_REQUIRED", "current "+kind+" allocation is unavailable")
			}
		}
		if kind == "gpu" {
			ratio, valid := new(big.Rat).SetString(fmt.Sprint(raw))
			if !valid || ratio.Sign() < 0 {
				return nil, domain.E("CURRENT_RESOURCES_REQUIRED", "current GPU allocation is invalid")
			}
			ratio.Quo(ratio, big.NewRat(100, 1))
			plain := decimalString(ratio)
			value := plain
			gpuType := scalarResource(node.Resources["gpuType"])
			memory := scalarResource(node.Resources["gpuMem"])
			if gpuType != "" {
				full := plain + ":" + gpuType + ":" + memory
				short := plain + ":" + gpuType
				if containsResource(resourceOptions(f), full) {
					value = full
				} else if containsResource(resourceOptions(f), short) {
					value = short
				}
			}
			out[f.Type] = value
		} else {
			value, e := resourceValue(kind, raw)
			if e != nil {
				return nil, e
			}
			out[f.Type] = value
		}
	}
	return out, nil
}
func fieldsByType(g resourceGroup) map[string]resourceField {
	out := map[string]resourceField{}
	for _, f := range g.Constraints {
		out[f.Type] = f
	}
	return out
}

// A partial selection may imply one complete CPU/Memory/GPU package. If several
// different packages remain possible, require another selector instead of guessing.
func selectResources(g resourceGroup, base, wanted map[string]string) (map[string]string, error) {
	fields := fieldsByType(g)
	result := map[string]string{}
	for k, v := range base {
		result[k] = v
	}
	for typ, value := range wanted {
		f, ok := fields[typ]
		if !ok {
			return nil, domain.E("RESOURCE_TYPE_UNKNOWN", "resource is not declared by the selected node group")
		}
		if value != f.Value && value != base[typ] && !containsResource(resourceOptions(f), value) {
			return nil, domain.E("RESOURCE_OPTION", "requested "+typ+" is not offered by this template")
		}
		result[typ] = value
	}
	bundle := []string{}
	count := -1
	aligned := true
	for _, typ := range []string{"CPU", "Memory", "GPU"} {
		f, ok := fields[typ]
		opts := resourceOptions(f)
		if !ok || len(opts) == 0 {
			continue
		}
		bundle = append(bundle, typ)
		if count < 0 {
			count = len(opts)
		} else if count != len(opts) {
			aligned = false
		}
	}
	changed := false
	for _, typ := range bundle {
		if value, ok := wanted[typ]; ok && value != base[typ] {
			changed = true
		}
	}
	if aligned && len(bundle) > 1 && changed {
		candidates := []map[string]string{}
		seen := map[string]bool{}
		for index := -1; index < count; index++ {
			row := map[string]string{}
			matches := true
			for _, typ := range bundle {
				if index < 0 {
					row[typ] = fields[typ].Value
				} else {
					row[typ] = resourceOptions(fields[typ])[index]
				}
				if value, ok := wanted[typ]; ok && value != row[typ] {
					matches = false
				}
			}
			if matches {
				b, _ := json.Marshal(row)
				if !seen[string(b)] {
					seen[string(b)] = true
					candidates = append(candidates, row)
				}
			}
		}
		if len(candidates) > 1 {
			preserving := []map[string]string{}
			for _, row := range candidates {
				matches := true
				for _, typ := range bundle {
					if _, changed := wanted[typ]; !changed && row[typ] != base[typ] {
						matches = false
					}
				}
				if matches {
					preserving = append(preserving, row)
				}
			}
			if len(preserving) == 1 {
				candidates = preserving
			}
		}
		if len(candidates) == 0 {
			return nil, domain.E("RESOURCE_COMBINATION", "requested resources do not match a template bundle")
		}
		if len(candidates) > 1 {
			return nil, domain.E("RESOURCE_COMBINATION_AMBIGUOUS", "selection matches different template bundles; specify additional resources")
		}
		for typ, value := range candidates[0] {
			result[typ] = value
		}
	}
	return result, nil
}
func serializeResources(g resourceGroup, values map[string]string) []map[string]string {
	out := []map[string]string{}
	for _, f := range g.Constraints {
		out = append(out, map[string]string{"type": f.Type, "value": values[f.Type]})
	}
	return out
}
func resourcesForSpec(spec domain.Spec, exact, resources map[string]any, current *domain.Node) (map[string]any, error) {
	if len(exact) > 0 && len(resources) > 0 {
		return nil, domain.E("RESOURCE_CONFLICT", "choose exact constraints or convenience flags")
	}
	groups, e := decodeResourceGroups(spec)
	if e != nil {
		return nil, e
	}
	if len(groups) == 0 {
		if len(exact) > 0 || len(resources) > 0 || current != nil {
			return nil, domain.E("TEMPLATE_CONSTRAINTS_REQUIRED", "template exposes no verifiable resource constraints")
		}
		return map[string]any{}, nil
	}
	wanted, e := decodeExactConstraints(exact, groups)
	if e != nil {
		return nil, e
	}
	base := map[string]map[string]string{}
	for gid, g := range groups {
		base[gid] = defaultResources(g)
	}
	currentID := ""
	if current != nil {
		if len(groups) != 1 {
			return nil, domain.E("CLONE_LAYOUT_UNSUPPORTED", "single-node cloning requires one unambiguous template group")
		}
		gid, g, e := groupForNode(groups, *current)
		if e != nil {
			return nil, e
		}
		base[gid], e = currentResources(g, *current)
		if e != nil {
			return nil, e
		}
		currentID = gid
	}
	if len(resources) > 0 {
		desired, e := normalizeResources(resources)
		if e != nil {
			return nil, e
		}
		candidates := []string{}
		for gid, g := range groups {
			if currentID != "" && gid != currentID {
				continue
			}
			fields := fieldsByType(g)
			matches := true
			for typ := range desired {
				if _, ok := fields[typ]; !ok {
					matches = false
				}
			}
			if matches {
				candidates = append(candidates, gid)
			}
		}
		if len(candidates) != 1 {
			return nil, domain.E("RESOURCE_GROUP_REQUIRED", "resource flags require one unambiguous template group; use exact constraints")
		}
		wanted[candidates[0]] = desired
	}
	out := map[string]any{}
	for gid, g := range groups {
		values, e := selectResources(g, base[gid], wanted[gid])
		if e != nil {
			return nil, e
		}
		out[gid] = serializeResources(g, values)
	}
	return out, nil
}
func (c *Controller) resourceConstraints(ctx context.Context, profile string, specID domain.ID, exact, resources map[string]any) (map[string]any, error) {
	api, e := c.api(profile)
	if e != nil {
		return nil, e
	}
	spec, e := api.Spec(ctx, specID)
	if e != nil {
		return nil, e
	}
	return resourcesForSpec(spec, exact, resources, nil)
}
func resizeResources(spec domain.Spec, node domain.Node, resources, exact map[string]any) (map[string]any, resourceGroup, error) {
	if len(resources) == 0 && len(exact) == 0 {
		return nil, resourceGroup{}, domain.E("RESOURCE_REQUIRED", "select at least one resource to change")
	}
	if len(resources) > 0 && len(exact) > 0 {
		return nil, resourceGroup{}, domain.E("RESOURCE_CONFLICT", "choose exact constraints or convenience flags")
	}
	groups, e := decodeResourceGroups(spec)
	if e != nil {
		return nil, resourceGroup{}, e
	}
	gid, g, e := groupForNode(groups, node)
	if e != nil {
		return nil, g, e
	}
	base, e := currentResources(g, node)
	if e != nil {
		return nil, g, e
	}
	wanted, e := normalizeResources(resources)
	if e != nil {
		return nil, g, e
	}
	if len(exact) > 0 {
		decoded, e := decodeExactConstraints(exact, groups)
		if e != nil {
			return nil, g, e
		}
		for other := range decoded {
			if other != gid {
				return nil, g, domain.E("RESOURCE_GROUP_REQUIRED", "resize constraints refer to a different node group")
			}
		}
		wanted = decoded[gid]
	}
	if _, requested := wanted["CPUArch"]; requested {
		return nil, g, domain.E("UNSUPPORTED_RESOURCE_CHANGE", "the native resize contract does not expose architecture changes")
	}
	values, e := selectResources(g, base, wanted)
	if e != nil {
		return nil, g, e
	}
	out := map[string]any{}
	for typ, value := range values {
		kind := wireKinds[typ]
		switch kind {
		case "cpu", "memory", "accelDisk":
			canonical, e := resourceValue(kind, value)
			if e != nil {
				return nil, g, e
			}
			out[kind] = json.Number(canonical)
		case "gpu":
			out[kind] = value
		case "bindCpu":
			canonical, e := resourceValue(kind, value)
			if e != nil {
				return nil, g, e
			}
			out[kind] = canonical
		}
	}
	return out, g, nil
}
func validateNumericResourceID(id domain.ID, label string, allowZero bool) error {
	if id == "" && allowZero {
		return nil
	}
	n, e := strconv.ParseUint(string(id), 10, 64)
	if e != nil || !allowZero && n == 0 || strconv.FormatUint(n, 10) != string(id) {
		return domain.E("INVALID_ID", label+" ID must be a numeric platform ID")
	}
	return nil
}
func instanceStringMeta(i domain.Instance, key string) string {
	value := i.Meta[key]
	if record, ok := value.(map[string]any); ok {
		value = record["value"]
	}
	text, _ := value.(string)
	return text
}
func requestedResourceMap(p Params, key string) (map[string]any, error) {
	if !p.Has(key) {
		return nil, nil
	}
	var out map[string]any
	d := json.NewDecoder(bytes.NewReader(p[key]))
	d.UseNumber()
	if e := d.Decode(&out); e != nil || out == nil {
		return nil, domain.E("INVALID_RESOURCES", key+" must be a JSON object")
	}
	return out, nil
}
