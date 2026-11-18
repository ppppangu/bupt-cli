package cli

import (
	"strings"
	"testing"
)

func TestComputeObjectCommandKinds(t *testing.T) {
	root := NewRoot(Options{})
	for _, tc := range []struct {
		cmd             string
		query, terminal bool
	}{
		{"compute quota request", false, false}, {"compute instance wait", false, true}, {"compute run attach", false, true}, {"compute run wait", false, true}, {"compute task wait", false, true},
		{"compute admin quota", true, false}, {"compute admin quota set", false, false}, {"compute remote order cancel", false, false}, {"compute catalog show", true, false},
	} {
		c, rest, e := root.Find(strings.Fields(tc.cmd))
		if e != nil || len(rest) != 0 {
			t.Fatal(tc.cmd, rest, e)
		}
		f := commandForm(c)
		if f.Query != tc.query || f.Terminal != tc.terminal {
			t.Fatalf("%s: %+v", tc.cmd, f)
		}
	}
}
