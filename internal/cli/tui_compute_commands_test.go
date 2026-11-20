package cli

import (
	"strings"
	"testing"
)

func TestComputeObjectCommandKinds(t *testing.T) {
	root := NewRoot(Options{})
	for _, path := range []string{"compute quota request", "compute instance wait", "compute job attach", "compute task wait", "admin quota show", "admin quota set", "remote order cancel", "compute catalog show"} {
		c, remaining, err := root.Find(strings.Fields(path))
		if err != nil || len(remaining) != 0 || !c.Runnable() {
			t.Fatal(path, remaining, err)
		}
	}
}
