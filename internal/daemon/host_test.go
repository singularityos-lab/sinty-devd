package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/singularityos-lab/sinty-devd/internal/db"
	"github.com/singularityos-lab/sinty-devd/internal/device"
	"github.com/singularityos-lab/sinty-devd/internal/rules"
)

func readHost(dir, id string) (map[string]string, []string, []string) {
	data, err := os.ReadFile(filepath.Join(dir, id))
	if err != nil {
		return nil, nil, nil
	}
	props := map[string]string{}
	var tags, links []string
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "E:"):
			k, v, _ := strings.Cut(line[2:], "=")
			props[k] = v
		case strings.HasPrefix(line, "G:"):
			tags = append(tags, line[2:])
		case strings.HasPrefix(line, "S:"):
			links = append(links, "/dev/"+line[2:])
		}
	}
	sort.Strings(tags)
	sort.Strings(links)
	return props, tags, links
}

func TestEngineAgainstHost(t *testing.T) {
	hostDB := os.Getenv("SINTY_DEVD_HOST_DB")
	hostRules := os.Getenv("SINTY_DEVD_HOST_RULES")
	hostHWDB := os.Getenv("SINTY_DEVD_HWDB")
	if hostDB == "" || hostRules == "" || hostHWDB == "" {
		t.Skip("set SINTY_DEVD_HOST_DB, SINTY_DEVD_HOST_RULES and SINTY_DEVD_HWDB")
	}
	db.Root = t.TempDir()
	rules.DBDir = filepath.Join(db.Root, "data")
	d, err := New(Config{Sys: "/sys", RulesDirs: []string{hostRules}, HWDB: hostHWDB, HelperDir: "/usr/lib/udev", NoRun: true})
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"ID_INPUT", "ID_INPUT_KEY", "ID_INPUT_KEYBOARD", "ID_INPUT_MOUSE", "ID_INPUT_TOUCHPAD", "ID_INPUT_POINTINGSTICK",
		"ID_PATH", "ID_PATH_TAG", "ID_BUS", "ID_SERIAL", "ID_MODEL", "ID_VENDOR", "ID_FS_TYPE", "ID_FS_UUID", "ID_PART_TABLE_TYPE",
		"ID_NET_NAME_PATH", "ID_NET_DRIVER", "SOUND_INITIALIZED", "ID_VENDOR_FROM_DATABASE", "ID_MODEL_FROM_DATABASE", "LIBINPUT_DEVICE_GROUP",
		"MOUSE_DPI", "ID_INPUT_WIDTH_MM", "ID_INPUT_HEIGHT_MM"}
	good, bad, compared := 0, 0, 0
	for _, class := range []string{"input", "block", "net", "sound", "drm", "video4linux"} {
		entries, _ := os.ReadDir("/sys/class/" + class)
		for _, e := range entries {
			link, err := os.Readlink(filepath.Join("/sys/class", class, e.Name()))
			if err != nil {
				continue
			}
			devpath := "/" + strings.TrimPrefix(filepath.Clean(filepath.Join("/sys/class", class, link)), "/sys/")
			dev, err := device.FromSyspath("/sys", devpath)
			if err != nil {
				continue
			}
			theirs, theirTags, theirLinks := readHost(hostDB, dev.ID())
			if theirs == nil {
				continue
			}
			env := map[string]string{"ACTION": "add"}
			for k, v := range dev.Props {
				env[k] = v
			}
			if err := d.Process(env); err != nil {
				t.Errorf("%s: %v", devpath, err)
				continue
			}
			compared++
			ours, ourTags, ourLinks, _, _ := db.Read(dev.ID())
			sort.Strings(ourTags)
			sort.Strings(ourLinks)
			for _, k := range keys {
				want, has := theirs[k]
				if !has {
					continue
				}
				if ours[k] == want {
					good++
				} else {
					bad++
					t.Errorf("%s/%s %s: got %q want %q", class, e.Name(), k, ours[k], want)
				}
			}
			if strings.Join(ourLinks, " ") != strings.Join(theirLinks, " ") {
				bad++
				t.Errorf("%s/%s links:\n got  %v\n want %v", class, e.Name(), ourLinks, theirLinks)
			} else {
				good++
			}
			if strings.Join(ourTags, ",") != strings.Join(theirTags, ",") {
				bad++
				t.Errorf("%s/%s tags: got %v want %v", class, e.Name(), ourTags, theirTags)
			} else {
				good++
			}
		}
	}
	t.Logf("devices %d, checks %d good, %d different", compared, good, bad)
}
