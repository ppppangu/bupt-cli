package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

func (a *app) emit(value any) error {
	if a.transferResults != nil {
		*a.transferResults = append(*a.transferResults, value)
		return nil
	}
	value = redact(value)
	if a.json {
		enc := json.NewEncoder(a.options.Out)
		enc.SetEscapeHTML(false)
		return enc.Encode(value)
	}
	if a.jsonl {
		enc := json.NewEncoder(a.options.Out)
		enc.SetEscapeHTML(false)
		if values, ok := value.([]any); ok {
			for _, v := range values {
				if err := enc.Encode(v); err != nil {
					return err
				}
			}
			return nil
		}
		return enc.Encode(value)
	}
	if a.quiet {
		return writeQuiet(a.options.Out, value)
	}
	return writeHuman(a.options.Out, value)
}
func sensitiveKey(k string) bool {
	k = strings.ToLower(strings.ReplaceAll(k, "-", "_"))
	for _, part := range []string{"password", "passphrase", "cookie", "token", "authorization", "private_key", "privatekey", "secret", "master_key"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}
func redact(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"error": "output cannot be encoded"}
	}
	var x any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if d.Decode(&x) != nil {
		return nil
	}
	var walk func(any) any
	walk = func(y any) any {
		switch z := y.(type) {
		case map[string]any:
			for k, val := range z {
				if sensitiveKey(k) && !publicMeter(k, val) {
					z[k] = "[redacted]"
				} else {
					z[k] = walk(val)
				}
			}
		case []any:
			for i := range z {
				z[i] = walk(z[i])
			}
		}
		return y
	}
	return walk(x)
}
func writeQuiet(w io.Writer, v any) error {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		for _, r := range x {
			if err := writeQuiet(w, r); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, k := range []string{"id", "task_id", "run_id", "name", "value"} {
			if val, ok := x[k]; ok {
				_, e := fmt.Fprintln(w, scalar(val))
				return e
			}
		}
		return fmt.Errorf("--quiet requires an identifier or scalar result")
	default:
		_, e := fmt.Fprintln(w, scalar(x))
		return e
	}
}
func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		return strings.NewReplacer("\n", "\\n", "\r", "\\r", "\t", "\\t", "\x1b", "\\x1b").Replace(x)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func writeHuman(w io.Writer, v any) error {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		if len(x) == 0 {
			_, err := fmt.Fprintln(w, "No results.")
			return err
		}
		maps := make([]map[string]any, 0, len(x))
		for _, row := range x {
			m, ok := row.(map[string]any)
			if !ok {
				return writePretty(w, v)
			}
			maps = append(maps, m)
		}
		cols := columnsFor(maps)
		t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for i, k := range cols {
			if i > 0 {
				fmt.Fprint(t, "\t")
			}
			fmt.Fprint(t, strings.ToUpper(k))
		}
		fmt.Fprintln(t)
		for _, row := range maps {
			for i, k := range cols {
				if i > 0 {
					fmt.Fprint(t, "\t")
				}
				fmt.Fprint(t, scalar(row[k]))
			}
			fmt.Fprintln(t)
		}
		return t.Flush()
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, k := range keys {
			fmt.Fprintf(t, "%s\t%s\n", k, scalar(x[k]))
		}
		return t.Flush()
	default:
		_, err := fmt.Fprintln(w, scalar(x))
		return err
	}
}
func writePretty(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func columnsFor(rows []map[string]any) []string {
	preferred := []string{"id", "name", "state", "health", "reserved", "expires_at", "ssh_state", "profile", "controller", "username", "version", "size", "type"}
	present := map[string]bool{}
	for _, row := range rows {
		for k, v := range row {
			if v == nil || reflect.TypeOf(v).Kind() != reflect.Map && reflect.TypeOf(v).Kind() != reflect.Slice {
				present[k] = true
			}
		}
	}
	out := []string{}
	for _, k := range preferred {
		if present[k] {
			out = append(out, k)
			delete(present, k)
		}
		if len(out) >= 7 {
			return out
		}
	}
	extra := []string{}
	for k := range present {
		extra = append(extra, k)
	}
	sort.Strings(extra)
	for _, k := range extra {
		out = append(out, k)
		if len(out) >= 7 {
			break
		}
	}
	if len(out) == 0 {
		return []string{"id"}
	}
	return out
}

// Only numeric billing measurements may pass through token redaction.
func publicMeter(key string, v any) bool {
	switch strings.ToLower(key) {
	case "max_tokens", "prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens", "periodtokens", "inputtokens", "outputtokens", "totaltokens", "tokens", "input_points_per_million_tokens", "output_points_per_million_tokens":
		if v == nil {
			return true
		}
		_, ok := v.(json.Number)
		return ok
	}
	return false
}
