package cli

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
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
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Slice && v.IsNil() && v.Type() != reflect.TypeFor[json.RawMessage]() {
		value = []any{}
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
	if a.ids {
		return writeIDs(a.options.Out, value)
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
func supportsID(cmd *cobra.Command) bool {
	switch cmd.CommandPath() {
	case "bupt api model", "bupt api model ls", "bupt api model show",
		"bupt api key", "bupt api key ls", "bupt api key show", "bupt api key create",
		"bupt compute catalog", "bupt compute catalog ls", "bupt compute catalog show",
		"bupt compute instance", "bupt compute instance ls", "bupt compute instance show", "bupt compute instance wait",
		"bupt compute instance create", "bupt compute instance start", "bupt compute instance stop", "bupt compute instance restart",
		"bupt compute instance release", "bupt compute instance restore", "bupt compute instance delete", "bupt compute instance cancel-queue",
		"bupt compute job", "bupt compute job ls", "bupt compute job show", "bupt compute job run", "bupt compute job wait", "bupt compute job cancel",
		"bupt compute task", "bupt compute task ls", "bupt compute task show", "bupt compute lease ls":
		return !cmd.Flags().Changed("columns")
	}
	return false
}

func writeIDs(w io.Writer, value any) error {
	if value == nil {
		return nil
	}
	rows, ok := value.([]any)
	if !ok {
		rows = []any{value}
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		obj, _ := row.(map[string]any)
		id := obj["operation_id"]
		if id == nil {
			id = obj["id"]
		}
		switch id.(type) {
		case string, json.Number:
			if id != "" {
				ids = append(ids, scalar(id))
				continue
			}
		}
		return usage("--output id 需要包含稳定 ID 的结果")
	}
	return writeIDLines(w, ids)
}

func writeIDLines[T ~string](w io.Writer, ids []T) error {
	for _, id := range ids {
		if _, err := fmt.Fprintln(w, scalar(string(id))); err != nil {
			return err
		}
	}
	return nil
}

func terminalWriter(w io.Writer, override func() bool) bool {
	if override != nil {
		return override()
	}
	return isTerminal(w)
}

func (a *app) humanOutput() bool {
	return !a.json && !a.jsonl && !a.ids && a.field == "" && a.interactive() &&
		terminalWriter(a.options.Err, a.options.StderrIsTerminal) && (a.options.Interactive != nil || isTerminal(a.options.Out))
}

func (a *app) humanHint(message string) {
	if a.humanOutput() {
		fmt.Fprintln(a.options.Err, message)
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
