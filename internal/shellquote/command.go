// Package shellquote renders displayable POSIX shell argv without evaluating them.
package shellquote

import "strings"

func Literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func Arg(s string) string {
	if s == "" || strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-/:=@", r))
	}) {
		return Literal(s)
	}
	return s
}
func Command(program string, args ...string) string {
	parts := []string{Arg(program)}
	for _, arg := range args {
		parts = append(parts, Arg(arg))
	}
	return strings.Join(parts, " ")
}
