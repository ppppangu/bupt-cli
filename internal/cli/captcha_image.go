package cli

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Kitty graphics are used only for an identified terminal, without a support
// query. q=2 on every chunk suppresses replies that could enter the CAPTCHA
// input. No pixels are interpreted or changed here.
func (a *app) inlineCaptchaPNG(data []byte) (bool, error) {
	stderrTTY := isTerminal(a.options.Err)
	if a.options.StderrIsTerminal != nil {
		stderrTTY = a.options.StderrIsTerminal()
	}
	if !a.interactive() || !stderrTTY || a.noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TMUX") != "" || os.Getenv("STY") != "" {
		return false, nil
	}
	terminal, program := strings.ToLower(os.Getenv("TERM")), strings.ToLower(os.Getenv("TERM_PROGRAM"))
	if terminal == "dumb" {
		return false, nil
	}
	if terminal != "xterm-ghostty" && terminal != "xterm-kitty" && program != "ghostty" && program != "kitty" {
		return false, nil
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return false, nil
	}
	columns, rows := 40, 7
	if f, ok := a.options.Err.(*os.File); ok {
		if width, height, e := term.GetSize(int(f.Fd())); e == nil {
			if width < 1 || height <= rows {
				return false, nil
			}
			columns = min(columns, width)
		}
	}
	if e := writeKittyPNG(a.options.Err, data, columns, rows); e != nil {
		return false, e
	}
	return true, nil
}

// Reserve blank lines before placement so the image fits even when the prompt
// starts at the terminal's bottom edge. C=1 keeps placement from changing the
// cursor; the final cursor movement leaves a fresh line for the user's answer.
func writeKittyPNG(out io.Writer, data []byte, columns, rows int) error {
	if columns < 1 || rows < 1 {
		return fmt.Errorf("invalid inline image placement")
	}
	if _, e := fmt.Fprintf(out, "%s\x1b[%dA\r", strings.Repeat("\n", rows), rows); e != nil {
		return e
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	for offset := 0; offset < len(encoded); offset += 4096 {
		end := min(offset+4096, len(encoded))
		more := 0
		if end < len(encoded) {
			more = 1
		}
		controls := fmt.Sprintf("q=2,m=%d", more)
		if offset == 0 {
			controls = fmt.Sprintf("a=T,f=100,t=d,q=2,c=%d,r=%d,C=1,m=%d", columns, rows, more)
		}
		if _, e := fmt.Fprintf(out, "\x1b_G%s;%s\x1b\\", controls, encoded[offset:end]); e != nil {
			return e
		}
	}
	_, e := fmt.Fprintf(out, "\x1b[%dB\r", rows)
	return e
}
