package control

import (
	"context"
	"io"
	"regexp"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

var consoleANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
var loginPrompt = regexp.MustCompile(`(?i)(?:^|[\r\n])[^\r\n]*\blogin:\s*$`)
var passwordPrompt = regexp.MustCompile(`(?i)(?:^|[\r\n])[^\r\n]*password:\s*$`)
var shellPrompt = regexp.MustCompile(`(?:^|[\r\n])[^\r\n]*[#$]\s*$`)

// AuthenticateConsole sends a password only after a login prompt and its
// matching password prompt. Output stays in memory and is never logged.
func AuthenticateConsole(ctx context.Context, console io.ReadWriteCloser, user, password string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		if _, e := io.WriteString(console, "\r"); e != nil {
			result <- e
			return
		}
		buffer := make([]byte, 0, 8192)
		chunk := make([]byte, 4096)
		sentUser, sentPassword := false, false
		for {
			n, e := console.Read(chunk)
			if e != nil {
				result <- e
				return
			}
			buffer = append(buffer, chunk[:n]...)
			if len(buffer) > 65536 {
				buffer = buffer[len(buffer)-32768:]
			}
			text := consoleANSI.ReplaceAllString(string(buffer), "")
			if shellPrompt.MatchString(text) {
				result <- nil
				return
			}
			if loginPrompt.MatchString(text) {
				if sentUser {
					result <- domain.E("CONSOLE_LOGIN_FAILED", "instance rejected console login; verify its password with bupt console")
					return
				}
				if user == "" || password == "" {
					result <- domain.E("CONSOLE_LOGIN_REQUIRED", "save the instance console password with bupt auth access, or inspect bupt console")
					return
				}
				if strings.ContainsAny(user+password, "\r\n\x00") {
					result <- domain.E("CONSOLE_LOGIN_REQUIRED", "credentials contain characters unsupported by terminal login")
					return
				}
				if _, e = io.WriteString(console, user+"\r"); e != nil {
					result <- e
					return
				}
				sentUser = true
				buffer = buffer[:0]
				continue
			}
			if passwordPrompt.MatchString(text) {
				if !sentUser || sentPassword {
					result <- domain.E("CONSOLE_LOGIN_REQUIRED", "console needs interactive login; run bupt console")
					return
				}
				if _, e = io.WriteString(console, password+"\r"); e != nil {
					result <- e
					return
				}
				sentPassword = true
				buffer = buffer[:0]
			}
		}
	}()
	select {
	case e := <-result:
		return e
	case <-ctx.Done():
		console.Close()
		return domain.E("CONSOLE_NOT_READY", "console shell was not ready; inspect bupt console and platform login")
	}
}
