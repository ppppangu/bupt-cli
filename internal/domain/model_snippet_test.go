package domain

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

// Exercise the exported programs as a student would with a local TLS gateway.
// The test credential is valid only in this fixture.
func TestRunnableGatewayExamples(t *testing.T) {
	const key = "fixture-key"
	for _, format := range []string{"curl", "python"} {
		t.Run(format, func(t *testing.T) {
			tool := "python3"
			if format == "curl" {
				tool = "curl"
			}
			if _, err := exec.LookPath(tool); err != nil {
				t.Skipf("%s unavailable", tool)
			}
			model := PortalModel{ID: "m", Kind: "rerank"}
			input := TrialInput{Query: "自定义问题\n第二行", Documents: []string{"甲", "乙"}}
			request, _ := model.TrialRequest(input)
			var sent, redirected atomic.Int64
			var redirect atomic.Bool
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/rerank" {
					redirected.Add(1)
					return
				}
				sent.Add(1)
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+key {
					t.Error("wrong method or credential")
				}
				var got, want map[string]any
				raw, _ := json.Marshal(request.Body)
				if json.Unmarshal(raw, &want) != nil || json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(want, got) {
					t.Errorf("payload mismatch: %#v", got)
				}
				if redirect.Load() {
					http.Redirect(w, r, "/redirected", http.StatusSeeOther)
					return
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			cert := filepath.Join(t.TempDir(), "local-ca.pem")
			if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			run := func() error {
				code, err := model.ExampleCode(input, format, "")
				if err != nil {
					t.Fatal(err)
				}
				var cmd *exec.Cmd
				if format == "curl" {
					cmd = exec.Command("sh", "-c", code)
				} else {
					cmd = exec.Command(tool, "-c", code)
				}
				cmd.Env = append(os.Environ(), "NO_PROXY=*", "no_proxy=*", "SSL_CERT_FILE="+cert, "CURL_CA_BUNDLE="+cert, "BUPT_BASE_URL="+server.URL+"/v1/", "BUPT_API_KEY="+key)
				return cmd.Run()
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			redirect.Store(true)
			_ = run()
			if sent.Load() != 2 || redirected.Load() != 0 {
				t.Fatalf("sent %d, followed %d redirects", sent.Load(), redirected.Load())
			}
		})
	}
}
