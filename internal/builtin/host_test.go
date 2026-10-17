package builtin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

func hostProps(t *testing.T, dir string, d *device.Device) map[string]string {
	data, err := os.ReadFile(filepath.Join(dir, d.ID()))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(strings.TrimPrefix(line, "E:"), "="); ok && strings.HasPrefix(line, "E:") {
			out[k] = v
		}
	}
	return out
}

func compare(t *testing.T, label string, ours, theirs map[string]string, keys []string) (int, int) {
	good, bad := 0, 0
	for _, k := range keys {
		want, has := theirs[k]
		got := ours[k]
		if !has {
			continue
		}
		if want == got {
			good++
			continue
		}
		bad++
		t.Errorf("%s %s: got %q want %q", label, k, got, want)
	}
	return good, bad
}

func TestAgainstHost(t *testing.T) {
	dir := os.Getenv("SINTY_DEVD_HOST_DB")
	if dir == "" {
		t.Skip("set SINTY_DEVD_HOST_DB to a copy of /run/udev/data")
	}
	reg := New("")
	total, wrong := 0, 0
	check := func(class string, builtins []string, keys []string) {
		entries, _ := os.ReadDir("/sys/class/" + class)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			link, err := os.Readlink(filepath.Join("/sys/class", class, e.Name()))
			if err != nil {
				continue
			}
			devpath := "/" + strings.TrimPrefix(filepath.Clean(filepath.Join("/sys/class", class, link)), "/sys/")
			d, err := device.FromSyspath("/sys", devpath)
			if err != nil {
				continue
			}
			theirs := hostProps(t, dir, d)
			if theirs == nil {
				continue
			}
			ours := map[string]string{}
			for _, b := range builtins {
				props, err := reg.Run(b, nil, d)
				if err != nil {
					continue
				}
				for k, v := range props {
					ours[k] = v
				}
			}
			g, b := compare(t, class+"/"+e.Name(), ours, theirs, keys)
			total += g + b
			wrong += b
		}
	}
	inputKeys := []string{"ID_INPUT", "ID_INPUT_KEY", "ID_INPUT_KEYBOARD", "ID_INPUT_MOUSE", "ID_INPUT_TOUCHPAD", "ID_INPUT_TOUCHSCREEN", "ID_INPUT_TABLET", "ID_INPUT_JOYSTICK", "ID_INPUT_SWITCH", "ID_INPUT_ACCELEROMETER", "ID_PATH", "ID_PATH_TAG"}
	check("input", []string{"input_id", "path_id"}, inputKeys)
	check("net", []string{"net_id", "path_id"}, []string{"ID_NET_NAME_PATH", "ID_NET_NAME_MAC", "ID_NET_NAME_ONBOARD", "ID_NET_NAME_SLOT", "ID_PATH", "ID_PATH_TAG"})
	check("block", []string{"path_id"}, []string{"ID_PATH", "ID_PATH_TAG"})
	check("sound", []string{"path_id"}, []string{"ID_PATH", "ID_PATH_TAG"})
	check("video4linux", []string{"path_id", "usb_id"}, []string{"ID_PATH", "ID_PATH_TAG", "ID_VENDOR_ID", "ID_MODEL_ID", "ID_SERIAL", "ID_USB_INTERFACES", "ID_TYPE", "ID_BUS"})
	t.Logf("compared %d properties, %d differ", total, wrong)
}

func TestHWDBAgainstHost(t *testing.T) {
	dir := os.Getenv("SINTY_DEVD_HOST_DB")
	hwdbPath := os.Getenv("SINTY_DEVD_HWDB")
	if dir == "" || hwdbPath == "" {
		t.Skip("set SINTY_DEVD_HOST_DB and SINTY_DEVD_HWDB")
	}
	db, err := OpenHWDB(hwdbPath)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"ID_VENDOR_FROM_DATABASE", "ID_MODEL_FROM_DATABASE", "ID_PCI_CLASS_FROM_DATABASE", "ID_PCI_SUBCLASS_FROM_DATABASE"}
	total, wrong := 0, 0
	for _, bus := range []string{"pci", "usb"} {
		entries, _ := os.ReadDir("/sys/bus/" + bus + "/devices")
		for _, e := range entries {
			link, err := os.Readlink(filepath.Join("/sys/bus", bus, "devices", e.Name()))
			if err != nil {
				continue
			}
			devpath := "/" + strings.TrimPrefix(filepath.Clean(filepath.Join("/sys/bus", bus, "devices", link)), "/sys/")
			d, err := device.FromSyspath("/sys", devpath)
			if err != nil {
				continue
			}
			modalias, ok := d.Attr("modalias")
			if !ok {
				continue
			}
			theirs := hostProps(t, dir, d)
			if theirs == nil {
				continue
			}
			ours := db.Lookup(modalias)
			g, b := compare(t, bus+"/"+e.Name(), ours, theirs, keys)
			total += g + b
			wrong += b
		}
	}
	t.Logf("compared %d hwdb properties, %d differ", total, wrong)
	if total == 0 {
		t.Fatal("nothing compared")
	}
}
