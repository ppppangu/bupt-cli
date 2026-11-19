package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func navPress(w *workspace, k tcell.Key) { w.handleKey(tcell.NewEventKey(k, 0, 0)); w.draw() }
func navType(w *workspace, text string) {
	for _, r := range text {
		w.handleKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
	}
	w.draw()
}
func navLine(w *workspace, y int) string {
	cols, _ := w.screen.Size()
	var b strings.Builder
	for x := 0; x < cols; {
		r, extra, _, n := w.screen.GetContent(x, y)
		b.WriteRune(r)
		b.WriteString(string(extra))
		x += max(1, n)
	}
	return strings.TrimSpace(b.String())
}

func TestRightBrowsesOnlyAndStaticMouseTargetsAreInert(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	browsed, sent := 0, 0
	w.local("控件", "", func() content {
		a := link("commit", "发送一次", "", "", func() { sent++ })
		a.verb = "发送一次"
		return content{rows: []row{info("状态", "就绪"), navLink("next", "只读详情", "", "", func() { browsed++ }), info("说明", "无需操作"), a}}
	})
	w.draw()
	if w.current().key != "next" {
		t.Fatal("static state took focus", w.current().key)
	}
	navPress(w, tcell.KeyRight)
	navPress(w, tcell.KeyDown)
	navPress(w, tcell.KeyRight)
	if browsed != 1 || sent != 0 || w.current().key != "commit" {
		t.Fatal(browsed, sent, w.current().key)
	}
	if !strings.Contains(navLine(w, 23), "Enter 发送一次") {
		t.Fatal(navLine(w, 23))
	}
	for _, xy := range [][2]int{{w.listLeft, w.listTop + 2}, {w.listLeft - 1, w.listTop + 3}, {w.listRight, w.listTop + 3}} {
		w.mouse(tcell.NewEventMouse(xy[0], xy[1], tcell.Button1, 0))
		w.mouse(tcell.NewEventMouse(xy[0], xy[1], tcell.ButtonNone, 0))
	}
	if sent != 0 {
		t.Fatal("nonaction hit selected a neighbouring submission")
	}
	s := w.screen.(tcell.SimulationScreen)
	s.SetSize(22, 8)
	w.draw()
	navPress(w, tcell.KeyEnter)
	if sent != 0 {
		t.Fatal("hidden final control submitted on tiny screen")
	}
	s.SetSize(80, 24)
	w.draw()
	navPress(w, tcell.KeyEnter)
	if sent != 1 {
		t.Fatal("explicit visible confirmation failed")
	}
}

func TestReaderScrollThenTailActionAndReturn(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	w.screen.(tcell.SimulationScreen).SetSize(42, 14)
	calls := 0
	w.local("阅读", "", func() content {
		v := w.textContent("第一段\n\n" + strings.Repeat("课程说明\n", 20) + "结尾")
		v.rows = append(v.rows, link("accept", "接受协议", "", "", func() { calls++ }))
		return v
	})
	w.draw()
	p := w.current()
	if p.key != "" || p.scroll != 0 || !strings.Contains(navLine(w, w.listTop), "第一段") {
		t.Fatalf("wrong initial reader: %+v", p)
	}
	navPress(w, tcell.KeyDown)
	if p.key != "" || p.scroll != 1 {
		t.Fatal("Down should scroll, including across a blank line")
	}
	navPress(w, tcell.KeyEnter)
	if calls != 0 {
		t.Fatal("reading submitted an action")
	}
	for i := 0; i < 30 && p.key != "accept"; i++ {
		navPress(w, tcell.KeyDown)
	}
	if p.key != "accept" {
		t.Fatal("action after the document was unreachable")
	}
	navPress(w, tcell.KeyRight)
	if calls != 0 {
		t.Fatal("reading direction submitted acceptance")
	}
	navPress(w, tcell.KeyEnter)
	if calls != 1 {
		t.Fatal("unable to activate visible action")
	}
	navPress(w, tcell.KeyUp)
	scroll := p.scroll
	if p.key != "" || scroll == 0 {
		t.Fatal("unable to return to the middle of the document")
	}
	navType(w, "/不存在")
	navPress(w, tcell.KeyEscape)
	if p.scroll != scroll || p.key != "" {
		t.Fatal("reader lost its place on clearing a filter", scroll, p.scroll, p.key)
	}
}

func TestFormAndFilterEditAtGraphemeCursor(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	var result string
	w.edit("文件名", "甲👩‍🔬e\u0301乙", func(s string) error { result = s; return nil })
	navPress(w, tcell.KeyLeft)
	navPress(w, tcell.KeyLeft)
	navPress(w, tcell.KeyBackspace2)
	navType(w, "新")
	navPress(w, tcell.KeyDelete)
	navPress(w, tcell.KeyHome)
	navType(w, "首")
	navPress(w, tcell.KeyEnd)
	navType(w, "尾")
	navPress(w, tcell.KeyEnter)
	if result != "首甲新乙尾" {
		t.Fatal(result)
	}
	w.local("页面", "", func() content {
		v := content{}
		for _, k := range []string{"abc1", "abxc", "abc2"} {
			v.rows = append(v.rows, navLink(k, k, "", "", func() {}))
		}
		return v
	})
	w.draw()
	navPress(w, tcell.KeyEnd)
	navType(w, "/abc")
	navPress(w, tcell.KeyEnter)
	navPress(w, tcell.KeyDown)
	if w.current().key != "abc2" {
		t.Fatal("failed to choose filtered row")
	}
	navType(w, "/")
	navPress(w, tcell.KeyLeft)
	navPress(w, tcell.KeyEnter)
	if w.current().key != "abc2" {
		t.Fatal("moving the filter caret changed the selected object")
	}
	navType(w, "/")
	navPress(w, tcell.KeyLeft)
	navType(w, "x")
	navPress(w, tcell.KeyEnter)
	if w.current().filter != "abxc" || w.current().key != "abxc" {
		t.Fatal(w.current().filter, w.current().key)
	}
	navPress(w, tcell.KeyBackspace2)
	if w.current().key != "abc2" || w.current().filter != "" {
		t.Fatal("did not return to original object")
	}
}

func TestMixedStatsOnSmallTerminalRemainReadable(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	w.screen.(tcell.SimulationScreen).SetSize(40, 13)
	w.local("近 30 天", "", func() content {
		v := content{rows: []row{link("period", "切换周期", "", "", func() {})}}
		for i := 0; i < 18; i++ {
			v.rows = append(v.rows, info(fmt.Sprintf("指标 %02d", i), "1 点"))
		}
		v.rows = append(v.rows, navLink("more", "每日明细", "", "", func() {}))
		return v
	})
	w.draw()
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		for y := w.listTop; y < w.listTop+w.listHeight; y++ {
			fields := strings.Fields(navLine(w, y))
			if len(fields) >= 2 {
				seen[strings.Join(fields[:2], " ")] = true
			}
		}
		if w.current().key == "more" {
			break
		}
		navPress(w, tcell.KeyDown)
	}
	for i := 0; i < 18; i++ {
		if !seen[fmt.Sprintf("指标 %02d", i)] {
			t.Fatalf("skipped metric %d", i)
		}
	}
	if w.current().key != "more" {
		t.Fatal("lost terminal action")
	}
}

func TestNarrowNamesFieldsAndSnippetHints(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	w.screen.(tcell.SimulationScreen).SetSize(28, 14)
	value := strings.Repeat("0123456789", 8)
	w.local("申请实验资源的个人账户", "", func() content {
		return content{rows: []row{info("模板 ID", value)}}
	})
	w.draw()
	click(t, w, "view/full-fields")
	var lines []string
	for _, r := range w.view(w.current()).rows {
		lines = append(lines, r.title)
	}
	if !strings.Contains(strings.Join(lines, ""), value) {
		t.Fatalf("lost a clipped value: %q", lines)
	}
	w.snippetPage("curl", "", "curl --help")
	w.draw()
	if line := navLine(w, 13); !strings.Contains(line, "Enter 复制") || !strings.Contains(line, "s 另存") {
		t.Fatal(line)
	}
}
