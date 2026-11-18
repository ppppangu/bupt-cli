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
		{nil, "bupt tui", "bupt            打开交互总览"},
		{[]string{"--help"}, "help resources", "--controller"},
		{[]string{"help", "compute"}, "instance", "--force-stop"},
		{[]string{"up", "--help"}, "--max-runtime", "--constraints"},
		{[]string{"help", "up", "--all"}, "--constraints", ""},
		{[]string{"help", "resources", "--all"}, "--cached", ""},
		{[]string{"help", "--all"}, "instance delete", ""},
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
	for noun, method := range map[string]string{"instance": "instance.list", "spec": "spec.list", "data": "data.config", "job": "job.list", "task": "task.list", "auth": "auth.status"} {
		if code := executeTest(t, opts, noun, "--json"); code != 0 || b.calls[len(b.calls)-1].method != method {
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
