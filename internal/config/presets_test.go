package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultPresetLayersAndDeletion(t *testing.T) {
	o := fixture(t)
	o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
	write(t, o.ConfigFile, `default_preset="global"
[presets.global]
spec_id="1"
[presets.personal]
spec_id="2"
[presets.project]
spec_id="3"
[profiles.bupt]
default_preset="personal"
`)
	c, e := Load(o)
	if e != nil || c.DefaultPreset != "personal" {
		t.Fatal(c, e)
	}
	o.ProjectFile = filepath.Join(o.WorkDir, ".bupt.toml")
	write(t, o.ProjectFile, "default_preset='project'\n")
	c, e = Load(o)
	if e != nil || c.DefaultPreset != "project" {
		t.Fatal(c, e)
	}
	o.Env["BUPT_PRESET"] = "global"
	c, e = Load(o)
	if e != nil || c.DefaultPreset != "global" {
		t.Fatal(c, e)
	}
	if e = UnsetFile(o.ConfigFile, "presets.global"); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(o); code(e) != "PRESET_NOT_FOUND" {
		t.Fatal(e)
	}
	delete(o.Env, "BUPT_PRESET")
	c, e = Load(o)
	if e != nil || c.DefaultPreset != "project" {
		t.Fatal(c, e)
	}
}

func TestUnselectedProfileRejectsInvalidPresetSettings(t *testing.T) {
	for _, setting := range []string{"default_preset=123", "[profiles.other.presets.bad]\nprivate_key='secret'"} {
		o := fixture(t)
		o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
		write(t, o.ConfigFile, "[profiles.other]\nplatform_url='https://other.example'\n"+setting+"\n")
		if _, e := Load(o); e == nil {
			t.Fatal("invalid unselected profile accepted", setting)
		}
	}
}
