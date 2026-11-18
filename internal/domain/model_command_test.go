package domain

import (
	"bytes"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestGeneratedBUPTCommandPreservesPayload(t *testing.T) {
	m := PortalModel{ID: "-my-model", Kind: "chat"}
	in := TrialInput{Texts: []string{"中文 ' \" $HOME \n换行"}, MaxTokens: 21}
	cmd, err := m.ExampleCode(in, "bupt", "")
	if err != nil {
		t.Fatal(err)
	}
	script := "bupt() { printf '%s\\0' \"$@\"; }; BUPT_KEY_ID='fixture key'; " + cmd
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(bytes.TrimSuffix(out, []byte{0})), "\x00")
	want := []string{"api", "model", "try", "--text", in.Texts[0], "--max-tokens", "21", "--key", "fixture key", "--", "-my-model"}
	if !slices.Equal(args, want) {
		t.Fatalf("%q", args)
	}
}
