// Package builtin implements the udev builtins the rules of the image use.
package builtin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

// Func computes properties for a device.
type Func func(args []string, d *device.Device) (map[string]string, error)

// Registry maps builtin names to implementations.
type Registry struct {
	funcs map[string]Func
	HWDB  *HWDB
}

// New returns the registry with every builtin. hwdbPath may be empty when no
// hardware database is installed.
func New(hwdbPath string) *Registry {
	r := &Registry{funcs: map[string]Func{}}
	if hwdbPath != "" {
		if db, err := OpenHWDB(hwdbPath); err == nil {
			r.HWDB = db
		}
	}
	r.funcs["kmod"] = kmod
	r.funcs["blkid"] = blkid
	r.funcs["btrfs"] = btrfs
	r.funcs["usb_id"] = usbID
	r.funcs["path_id"] = pathID
	r.funcs["input_id"] = inputID
	r.funcs["net_id"] = netID
	r.funcs["keyboard"] = keyboard
	r.funcs["hwdb"] = r.hwdb
	return r
}

// Run executes the named builtin.
func (r *Registry) Run(name string, args []string, d *device.Device) (map[string]string, error) {
	f, ok := r.funcs[name]
	if !ok {
		return nil, fmt.Errorf("unknown builtin %q", name)
	}
	return f(args, d)
}

// Has reports whether a builtin exists.
func (r *Registry) Has(name string) bool {
	_, ok := r.funcs[name]
	return ok
}

func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	err := cmd.Run()
	return out.String(), err
}

func kmod(args []string, d *device.Device) (map[string]string, error) {
	if len(args) == 0 || args[0] != "load" {
		return nil, nil
	}
	aliases := args[1:]
	if len(aliases) == 0 && d.Props["MODALIAS"] != "" {
		aliases = []string{d.Props["MODALIAS"]}
	}
	for _, alias := range aliases {
		if alias == "" {
			continue
		}
		if _, err := run(30*time.Second, "/sbin/modprobe", "-b", "-q", "--", alias); err != nil {
			if _, err := exec.LookPath("modprobe"); err == nil {
				run(30*time.Second, "modprobe", "-b", "-q", "--", alias)
			}
		}
	}
	return nil, nil
}

func blkid(args []string, d *device.Device) (map[string]string, error) {
	if d.DevNode == "" {
		return nil, fmt.Errorf("no device node")
	}
	out, err := run(10*time.Second, "blkid", "-p", "-o", "udev", d.DevNode)
	if err != nil && out == "" {
		return nil, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.HasPrefix(k, "ID_") {
			props[k] = v
		}
	}
	if len(props) == 0 {
		return nil, fmt.Errorf("nothing found")
	}
	return props, nil
}

func btrfs(args []string, d *device.Device) (map[string]string, error) {
	if len(args) == 0 || args[0] != "ready" || len(args) < 2 {
		return nil, fmt.Errorf("btrfs ready needs a device")
	}
	ready, err := btrfsReady(args[1])
	if err != nil {
		return map[string]string{"ID_BTRFS_READY": "1"}, nil
	}
	if ready {
		return map[string]string{"ID_BTRFS_READY": "1"}, nil
	}
	return map[string]string{"ID_BTRFS_READY": "0"}, nil
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// encodeString escapes a sysfs string the way udev does for *_ENC properties.
func encodeString(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == ' ' || c == '\\' || c == '"' || c == '\'' || c == '`' || c >= 0x7f {
			fmt.Fprintf(&b, "\\x%02x", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// replaceChars keeps characters safe for use in a symlink name.
func replaceChars(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', strings.ContainsRune("#+-.:=@_", r):
			b.WriteRune(r)
		case r > 0x7f:
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func replaceWhitespace(s string) string {
	return strings.Join(strings.Fields(s), "_")
}
