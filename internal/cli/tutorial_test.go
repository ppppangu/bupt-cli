package cli

import (
	"strings"
	"testing"
)

func TestOfflineHelpTutorialUsesNoController(t *testing.T) {
	b := &fakeBackend{}
	opts, out, diag := testOptions(t, b)
	for _, topic := range []string{"", "api", "cli", "keys"} {
		args := []string{"help", "tutorial"}
		if topic != "" {
			args = append(args, topic)
		}
		out.Reset()
		if rc := executeTest(t, opts, args...); rc != 0 || out.Len() == 0 || len(b.calls) > 0 {
			t.Fatal(rc, diag.String())
		}
	}
	if rc := executeTest(t, opts, "help", "tutorial", "unknown"); rc != 2 || !strings.Contains(diag.String(), "unknown") {
		t.Fatal(rc)
	}
}
