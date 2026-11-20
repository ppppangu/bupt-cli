package tui

import (
	"time"

	"bupt.local/cli/internal/tuiprefs"
)

func (w *workspace) readDraft(slot string) tuiprefs.Draft {
	if slot == "" || w.Config.Paths.StateDir == "" {
		return tuiprefs.Draft{}
	}
	d, err := tuiprefs.ReadDraft(w.Config.Paths.StateDir, w.Config.Selected+"/"+slot)
	if err != nil {
		w.notice = "本地草稿无法恢复，可继续编辑或清除草稿"
	}
	return d
}

func (w *workspace) writeDraft(slot string, d tuiprefs.Draft) {
	if !w.Cached && slot != "" && w.Config.Paths.StateDir != "" {
		if err := tuiprefs.WriteDraft(w.Config.Paths.StateDir, w.Config.Selected+"/"+slot, d); err != nil {
			w.notice = "草稿未能保存到本机，当前会话仍可继续编辑"
		}
	}
}

func (w *workspace) clearDraft(slot string) bool {
	if slot == "" || w.Config.Paths.StateDir == "" {
		return true
	}
	if err := tuiprefs.ClearDraft(w.Config.Paths.StateDir, w.Config.Selected+"/"+slot); err != nil {
		w.notice = "无法清除本机草稿"
		return false
	}
	return true
}

func (d *draft) slot() string {
	if d.instance != nil {
		return ""
	}
	if d.preset != nil {
		return "create/preset/" + d.preset.Name + "/" + string(d.preset.SpecID)
	}
	if d.spec.ID != "" {
		return "create/spec/" + string(d.spec.ID)
	}
	return ""
}

func (w *workspace) restoreCreateDraft(d *draft) {
	saved := w.readDraft(d.slot())
	if !saved.Updated.IsZero() {
		d.limit, d.allocateOnly = saved.Runtime, saved.AllocateOnly
	}
}

func (w *workspace) saveCreateDraft(d *draft) {
	runtime := ""
	if limit, err := time.ParseDuration(d.limit); err == nil && limit > 0 {
		runtime = limit.String()
	}
	w.writeDraft(d.slot(), tuiprefs.Draft{Runtime: runtime, AllocateOnly: d.allocateOnly})
}

func (w *workspace) saveModelDraft(f *trialForm) {
	w.writeDraft("model/"+w.draftSlot(f), tuiprefs.Draft{MaxTokens: f.input.MaxTokens, TopN: f.input.TopN})
}
