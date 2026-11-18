package platform

import (
	"strings"
	"testing"
)

func TestGuideUsesPublicValidatedGateway(t *testing.T) {
	const official = "https://myai.bupt.edu.cn/llm-gw/v1"
	for _, bad := range []string{official + "?", official + "?a=b", official + "/../v1", "https://myai.bupt.edu.cn:444/llm-gw/v1", "https://somewhere.invalid/v1"} {
		if _, err := ModelGuideData(bad, ""); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	guide, err := ModelGuideData(official+"/", "")
	if err != nil || guide["base_url"] != official || !strings.Contains(guide["curl"].(string), official+"/chat/completions") || !strings.Contains(guide["python"].(string), "BUPT_MODEL") {
		t.Fatalf("guide: %v %v", guide, err)
	}
}
