package domain

import (
	"encoding/json"
	"testing"
)

func TestPortalDocumentLineBreaksAndMarkdownCode(t *testing.T) {
	for _, body := range []struct{ in, want string }{
		{"甲\r\n乙\n丙", "甲\n乙\n丙"},
		{`甲\r\n乙\n丙`, "甲\n乙\n丙"},
		{"甲\\n`printf \\n`\\n" + "~~~\n\\n\n~~~", "甲\n`printf \\n`\n~~~\n\\n\n~~~"},
		{`保留 \\n 与 C:\tmp，段尾\n下一段`, "保留 \\\\n 与 C:\\tmp，段尾\n下一段"},
	} {
		for _, field := range []string{"body", "bodyMd"} {
			raw, _ := json.Marshal(map[string]any{"data": map[string]any{"id": "one", "title": "标题", field: body.in}})
			var got PortalContent[PortalDocument]
			if err := json.Unmarshal(raw, &got); err != nil || got.Data.Body != body.want {
				t.Fatalf("%s %q: %q %v", field, body.in, got.Data.Body, err)
			}
		}
	}
}
func TestNewsContractDistinguishesEmptyFromInvalid(t *testing.T) {
	for _, v := range []struct {
		wire  string
		valid bool
	}{
		{`{"data":{"items":[],"nextCursor":null,"unreadCount":0,"unseenCount":0}}`, true},
		{`{"data":{}}`, false}, {`{}`, false}, {`{"data":{"items":null}}`, false},
		{`{"data":{"items":[{"title":"broken"}]}}`, false},
	} {
		var got PortalContent[PortalNews]
		if err := json.Unmarshal([]byte(v.wire), &got); (err == nil) != v.valid {
			t.Fatal(v.wire, err)
		}
	}
}
