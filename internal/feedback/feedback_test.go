package feedback

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

func TestContactIsSyncedToREADME(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	block := "<!-- feedback-contact:start -->\n**联系邮箱** [" + Email + "](mailto:" + Email + ")\n<!-- feedback-contact:end -->"
	if strings.Count(string(data), block) != 1 {
		t.Fatal("README contact is out of sync; run go generate ./internal/feedback")
	}
}

func TestDraftIsBuiltFromAllowlistedFields(t *testing.T) {
	const id = "d81283c2-ae5f-4d03-aaee-2437342f29c3"
	const password = "FAKE_UPPERCASE_PASSWORD"
	raw := &domain.Error{
		Code: "SSH_DIAL_FAILED", RequestID: id, RunID: "https://private.example/secret",
		TaskID: "sk-fake-api-key", Message: "password=" + password + " /Users/private/.ssh/id_ed25519",
		Hint: "Authorization: Bearer fake-token", IdempotencyKey: "fake-private-key",
	}
	issue := FromError("ssh", raw)
	snapshot := Collect(t.Context(), "dev", nil, func(ctx context.Context) error {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Error("unbounded diagnostic")
		}
		return errors.New("connection refused on /Users/private/.bupt")
	})
	body := (Draft{Category: issue.Category(), Incident: issue, Version: "dev", Report: &snapshot}).Text()
	for _, valid := range []string{Email, "SSH 连接失败", "bupt ssh", raw.Code, id, "无法连接"} {
		if !strings.Contains(body, valid) {
			t.Errorf("missing safe field %q", valid)
		}
	}
	for _, private := range []string{password, "private.example", "sk-fake", "/Users/private", "fake-token", "fake-private-key"} {
		if strings.Contains(body, private) {
			t.Errorf("diagnostic copied a private field")
		}
	}
	if got := SafeCode(password); got != "ERROR" {
		t.Fatal("an unregistered code was copied")
	}
	if got := FromError("bupt ssh private-instance", raw).Operation; got != "" {
		t.Fatal("raw arguments entered the report")
	}
}
