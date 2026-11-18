package wire

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestReadJSONLimitWithoutNewline(t *testing.T) {
	r := bufio.NewReaderSize(io.MultiReader(strings.NewReader(strings.Repeat("x", MaxMessage+1)), strings.NewReader("\n")), 64)
	var out any
	if ReadJSON(r, &out) == nil {
		t.Fatal("oversized JSON accepted")
	}
}
func TestFramesPreserveBinary(t *testing.T) {
	var b bytes.Buffer
	w := &Writer{W: &b}
	want := []byte{0, 1, 255, 10}
	if e := w.Frame(Output, want); e != nil {
		t.Fatal(e)
	}
	k, p, e := ReadFrame(&b)
	if e != nil || k != Output || !bytes.Equal(p, want) {
		t.Fatal("binary framing changed bytes")
	}
}
