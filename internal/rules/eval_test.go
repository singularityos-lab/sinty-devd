package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

type fakeHost struct{ builtins map[string]map[string]string }

func (h fakeHost) Builtin(name string, args []string, d *device.Device) (map[string]string, error) {
	if v, ok := h.builtins[name]; ok {
		return v, nil
	}
	return nil, os.ErrNotExist
}

func (h fakeHost) Program(command string, d *device.Device) (string, error) {
	if strings.HasPrefix(command, "echo ") {
		return strings.TrimPrefix(command, "echo ") + "\n", nil
	}
	return "", os.ErrNotExist
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeSys(t *testing.T) string {
	sys := t.TempDir()
	parent := filepath.Join(sys, "devices/platform/i8042/serio1/input/input5")
	writeFile(t, filepath.Join(parent, "uevent"), "PRODUCT=11/2/7/1b1\nNAME=\"SynPS/2 Synaptics TouchPad\"\n")
	writeFile(t, filepath.Join(parent, "name"), "SynPS/2 Synaptics TouchPad\n")
	writeFile(t, filepath.Join(parent, "event5/uevent"), "MAJOR=13\nMINOR=69\nDEVNAME=input/event5\n")
	os.MkdirAll(filepath.Join(sys, "class/input"), 0o755)
	os.Symlink(filepath.Join(sys, "class/input"), filepath.Join(parent, "subsystem"))
	os.Symlink(filepath.Join(sys, "class/input"), filepath.Join(parent, "event5/subsystem"))
	return sys
}

func TestApply(t *testing.T) {
	sys := fakeSys(t)
	set := &Set{}
	text := `SUBSYSTEM!="input", GOTO="end"
KERNEL=="event*", ATTRS{name}=="*TouchPad*", ENV{ID_INPUT_TOUCHPAD}="1", SYMLINK+="input/by-name/%s{name}-%k"
SUBSYSTEM=="input", GROUP="input", MODE="0660"
KERNEL=="event*", GROUP:="video"
KERNEL=="event*", GROUP="ignored"
IMPORT{builtin}=="input_id", ENV{SEEN}="yes"
PROGRAM=="echo one two three", RESULT=="one *", ENV{SECOND}="%c{2}", ENV{REST}="%c{2+}"
ENV{ID_INPUT_TOUCHPAD}=="1", TAG+="uaccess", RUN+="/bin/true %k"
LABEL="end"
ENV{AFTER}="1"
`
	rules, err := Parse(strings.NewReader(text), "t.rules")
	if err != nil {
		t.Fatal(err)
	}
	set.Rules = rules
	set.resolve()
	d, err := device.FromSyspath(sys, "/devices/platform/i8042/serio1/input/input5/event5")
	if err != nil {
		t.Fatal(err)
	}
	d.Action = "add"
	out := set.Apply(d, fakeHost{builtins: map[string]map[string]string{"input_id": {"ID_INPUT": "1"}}})
	if d.Subsystem != "input" || d.DevNode != "/dev/input/event5" || d.ID() != "c13:69" {
		t.Fatalf("device %s %s %s", d.Subsystem, d.DevNode, d.ID())
	}
	if d.Props["ID_INPUT_TOUCHPAD"] != "1" || d.Props["ID_INPUT"] != "1" || d.Props["SEEN"] != "yes" {
		t.Fatalf("props %v", d.Props)
	}
	if out.Group != "video" || out.Mode != 0o660 || !out.ModeSet {
		t.Fatalf("group %q mode %o", out.Group, out.Mode)
	}
	if d.Props["SECOND"] != "two" || d.Props["REST"] != "two three" {
		t.Fatalf("result %q %q", d.Props["SECOND"], d.Props["REST"])
	}
	if !d.Tags["uaccess"] || len(out.Run) != 1 || out.Run[0] != "/bin/true event5" {
		t.Fatalf("tags %v run %v", d.Tags, out.Run)
	}
	if _, ok := d.Links["/dev/input/by-name/SynPS/2_Synaptics_TouchPad-event5"]; !ok {
		t.Fatalf("links %v", d.Links)
	}
	if d.Props["AFTER"] != "1" {
		t.Fatalf("rules after the label did not run")
	}

	other := &device.Device{}
	*other = *device.New(sys, "/devices/virtual/net/lo")
	other.Subsystem = "net"
	other.Action = "add"
	set.Apply(other, fakeHost{})
	if other.Props["ID_INPUT_TOUCHPAD"] != "" || other.Props["AFTER"] != "1" {
		t.Fatalf("GOTO did not skip: %v", other.Props)
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"event*", "event5", true},
		{"tty[0-9]*", "tty3", true},
		{"tty[0-9]*", "ttyS0", false},
		{"sd*|vd*", "vda", true},
		{"*/by-id/*", "disk/by-id/x", true},
		{"input/*", "input/event5", true},
		{"exact", "exact", true},
		{"exact", "exactly", false},
		{"ID_[!F]*", "ID_PATH", true},
		{"ID_[!F]*", "ID_FS_TYPE", false},
	}
	for _, c := range cases {
		if Glob(c.pattern, c.value) != c.want {
			t.Errorf("Glob(%q, %q) != %v", c.pattern, c.value, c.want)
		}
	}
}

func TestForeignAttr(t *testing.T) {
	sys := fakeSys(t)
	writeFile(t, filepath.Join(sys, "class/dmi/id/modalias"), "dmi:bvnLENOVO:pnThinkPad\n")
	rules, err := Parse(strings.NewReader(`KERNEL=="event*", ATTR{[dmi/id]modalias}=="dmi:*ThinkPad*", ENV{KB}="$attr{[dmi/id]modalias}"`+"\n"), "t.rules")
	if err != nil {
		t.Fatal(err)
	}
	set := &Set{Rules: rules}
	set.resolve()
	d, _ := device.FromSyspath(sys, "/devices/platform/i8042/serio1/input/input5/event5")
	set.Apply(d, fakeHost{})
	if d.Props["KB"] != "dmi:bvnLENOVO:pnThinkPad" {
		t.Fatalf("KB = %q", d.Props["KB"])
	}
}
