package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
)

type kittyFrame struct {
	controls map[string]string
	payload  string
}

func parseKittyFrames(t *testing.T, text string) []kittyFrame {
	t.Helper()
	frames := []kittyFrame{}
	for {
		start := strings.Index(text, "\x1b_G")
		if start < 0 {
			break
		}
		text = text[start+3:]
		end := strings.Index(text, "\x1b\\")
		if end < 0 {
			t.Fatal("unterminated Kitty graphics frame")
		}
		control, payload, ok := strings.Cut(text[:end], ";")
		if !ok {
			t.Fatal("graphics frame omitted payload separator")
		}
		keys := map[string]string{}
		for _, entry := range strings.Split(control, ",") {
			k, v, ok := strings.Cut(entry, "=")
			if !ok {
				t.Fatalf("malformed graphics control %q", entry)
			}
			keys[k] = v
		}
		frames = append(frames, kittyFrame{keys, payload})
		text = text[end+2:]
	}
	return frames
}
func terminalEnv(t *testing.T, terminal, program, tmux, noColor string) {
	t.Helper()
	for key, value := range map[string]string{"TERM": terminal, "TERM_PROGRAM": program, "TMUX": tmux, "STY": "", "NO_COLOR": noColor} {
		t.Setenv(key, value)
	}
}

func TestKittyPNGChunksPreserveBytesAndSuppressAllReplies(t *testing.T) {
	data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0, 0xff, 0x42, 0x1b}, 2400)...)
	var out bytes.Buffer
	if e := writeKittyPNG(&out, data, 40, 7); e != nil {
		t.Fatal(e)
	}
	frames := parseKittyFrames(t, out.String())
	if len(frames) < 3 {
		t.Fatalf("large image was not chunked: %d frames", len(frames))
	}
	var payload strings.Builder
	for i, frame := range frames {
		if len(frame.payload) > 4096 || len(frame.payload)%4 != 0 {
			t.Fatalf("invalid base64 chunk length: %d", len(frame.payload))
		}
		if frame.controls["q"] != "2" {
			t.Fatalf("frame %d could inject a terminal reply into CAPTCHA input", i)
		}
		wantMore := "1"
		if i == len(frames)-1 {
			wantMore = "0"
		}
		if frame.controls["m"] != wantMore {
			t.Fatalf("invalid final/continuation marker at frame %d", i)
		}
		if i == 0 {
			for key, want := range map[string]string{"a": "T", "f": "100", "t": "d", "c": "40", "r": "7", "C": "1"} {
				if frame.controls[key] != want {
					t.Fatalf("first frame %s=%q, want %q", key, frame.controls[key], want)
				}
			}
		} else {
			if len(frame.controls) != 2 {
				t.Fatalf("continuation repeated full image controls: %+v", frame.controls)
			}
		}
		payload.WriteString(frame.payload)
	}
	decoded, e := base64.StdEncoding.DecodeString(payload.String())
	if e != nil || !bytes.Equal(decoded, data) {
		t.Fatal("transmitted image bytes changed")
	}
	for _, query := range []string{"a=q", "\x1b[c", "\x1b[6n", "\x1b[14t", "\x1b[16t"} {
		if strings.Contains(out.String(), query) {
			t.Fatal("image display issued a terminal query requiring an stdin response")
		}
	}
	if !strings.HasSuffix(out.String(), "\x1b[7B\r") {
		t.Fatal("cursor was not restored below the image")
	}
}

func TestInlineCaptchaNeedsRecognizedInteractiveStderrTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, terminal, program, tmux, noColor    string
		interactive, stderrTTY, flagNoColor, want bool
	}{
		{"Ghostty", "xterm-ghostty", "", "", "", true, true, false, true},
		{"Ghostty program", "xterm-256color", "ghostty", "", "", true, true, false, true},
		{"kitty", "xterm-kitty", "", "", "", true, true, false, true},
		{"kitty program", "xterm-256color", "kitty", "", "", true, true, false, true},
		{"redirected stderr", "xterm-ghostty", "", "", "", true, false, false, false},
		{"noninteractive", "xterm-ghostty", "", "", "", false, true, false, false},
		{"tmux", "xterm-ghostty", "ghostty", "/tmp/tmux-session", "", true, true, false, false},
		{"no color flag", "xterm-ghostty", "", "", "", true, true, true, false},
		{"no color env", "xterm-ghostty", "", "", "1", true, true, false, false},
		{"unsupported", "xterm-256color", "other", "", "", true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminalEnv(t, tc.terminal, tc.program, tc.tmux, tc.noColor)
			var errOut bytes.Buffer
			a := &app{options: Options{Err: &errOut, Interactive: func() bool { return tc.interactive }}, noColor: tc.flagNoColor}
			// The negative stderr case uses the real default check on a non-TTY
			// writer, rather than pretending that a redirected stream is a TTY.
			if tc.stderrTTY {
				a.options.StderrIsTerminal = func() bool { return true }
			}
			shown, e := a.inlineCaptchaPNG([]byte("\x89PNG\r\n\x1a\nimage"))
			if e != nil || shown != tc.want {
				t.Fatalf("shown=%v want=%v err=%v", shown, tc.want, e)
			}
			if !tc.want && errOut.Len() != 0 {
				t.Fatal("graphics escaped into an unsupported or redirected output")
			}
		})
	}
}

func TestInlineLoginDisplaysEachNewCaptchaWithoutOpeningViewer(t *testing.T) {
	terminalEnv(t, "xterm-ghostty", "ghostty", "", "")
	images := [][]byte{[]byte("\x89PNG\r\n\x1a\nfirst synthetic image"), []byte("\x89PNG\r\n\x1a\nsecond synthetic image")}
	logins, challenges := 0, 0
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "auth.status":
			return domain.AuthStatus{Gateway: "authenticated"}, nil
		case "auth.captcha":
			i := challenges
			challenges++
			return map[string]any{"uuid": fmt.Sprintf("opaque-%d", i), "image": base64.StdEncoding.EncodeToString(images[i])}, nil
		case "auth.login":
			logins++
			if logins == 2 && p["captcha_code"] != "first-answer" {
				t.Fatal("first answer was changed by protocol input")
			}
			if logins == 3 && p["captcha_code"] != "second-answer" {
				t.Fatal("second answer was changed by protocol input")
			}
			if logins < 3 {
				return nil, domain.E("CAPTCHA_REQUIRED", "new CAPTCHA required")
			}
			return map[string]any{"authenticated": true}, nil
		default:
			t.Fatalf("unexpected RPC %s", method)
			return nil, nil
		}
	}}
	opts, out, errOut := testOptions(t, b)
	opts.Interactive = func() bool { return true }
	opts.StderrIsTerminal = func() bool { return true }
	opts.ReadSecret = func(string) (string, error) { return "test-password", nil }
	opts.In = strings.NewReader("first-answer\nsecond-answer\n")
	opts.OpenImage = func(context.Context, string) error { t.Fatal("inline display launched an external viewer"); return nil }
	if code := executeTest(t, opts, "auth", "login", "--local", "--username", "student", "--json"); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !json.Valid(out.Bytes()) || strings.Contains(out.String(), "\x1b") {
		t.Fatal("terminal graphics contaminated stdout")
	}
	frames := parseKittyFrames(t, errOut.String())
	if len(frames) != 2 {
		t.Fatalf("wanted two fresh images, got %d", len(frames))
	}
	for i, frame := range frames {
		data, e := base64.StdEncoding.DecodeString(frame.payload)
		if e != nil || !bytes.Equal(data, images[i]) {
			t.Fatalf("displayed stale or changed CAPTCHA at attempt %d", i+1)
		}
	}
}
