package cli

import (
	"strings"
	"testing"
)

func TestProgressiveHelpExposesAdvancedFlagsOnDemand(t *testing.T) {
	opts, out, errout := testOptions(t, &fakeBackend{})
	for _, test := range []struct {
		args       []string
		want, omit string
	}{
		{nil, "打开交互工作台", "admin"},
		{nil, "login", "  up "},
		{[]string{"help", "access"}, "logout", ""},
		{[]string{"logout", "--help"}, "--force", ""},
		{[]string{"--help"}, "help COMMAND", "--controller"},
		{[]string{"help", "compute"}, "instance", "admin"},
		{[]string{"compute", "instance", "create", "--help"}, "--max-runtime", "--constraints"},
		{[]string{"help", "compute", "instance", "create", "--all"}, "--constraints", ""},
		{[]string{"help", "compute", "catalog", "--all"}, "--cached", ""},
		{[]string{"help", "--all"}, "compute instance delete", "compute run"},
	} {
		out.Reset()
		if code := executeTest(t, opts, test.args...); code != 0 || !strings.Contains(out.String(), test.want) || test.omit != "" && strings.Contains(out.String(), test.omit) {
			t.Fatal(test, code, out.String(), errout.String())
		}
	}
}
func TestBareObjectDispatchAndNonTTYContracts(t *testing.T) {
	b := &fakeBackend{}
	opts, out, errout := testOptions(t, b)
	for noun, method := range map[string]string{"compute instance": "instance.list", "compute catalog": "resources.overview", "compute storage": "data.config", "compute job": "job.list", "auth": "auth.status"} {
		args := append(strings.Fields(noun), "--json")
		if code := executeTest(t, opts, args...); code != 0 || b.calls[len(b.calls)-1].method != method {
			t.Fatal(noun, code, errout.String())
		}
	}
	out.Reset()
	before := len(b.calls)
	if code := executeTest(t, opts); code != 0 || len(b.calls) != before || !strings.Contains(out.String(), "按用途浏览") {
		t.Fatal(code, out.String())
	}
	if code := executeTest(t, opts, "tui", "--json"); code != 2 || len(b.calls) != before {
		t.Fatal("formatted TUI opened a connection")
	}
}
