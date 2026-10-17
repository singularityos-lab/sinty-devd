// Package device models a kernel device as seen through sysfs and uevents.
package device

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Device is one kernel device and the properties gathered for it.
type Device struct {
	Sys       string
	DevPath   string
	Action    string
	Subsystem string
	DevType   string
	Driver    string
	SysName   string
	DevNode   string
	Major     int
	Minor     int
	IfIndex   int
	Seqnum    uint64
	Props     map[string]string
	Tags      map[string]bool
	Links     map[string]int

	parent    *Device
	parentSet bool
	attrs     map[string]*string
}

// New returns an empty device rooted at sys (normally "/sys").
func New(sys, devpath string) *Device {
	d := &Device{
		Sys:     sys,
		DevPath: devpath,
		Props:   map[string]string{},
		Tags:    map[string]bool{},
		Links:   map[string]int{},
		attrs:   map[string]*string{},
		Major:   -1,
		Minor:   -1,
	}
	d.SysName = sysname(devpath)
	return d
}

func sysname(devpath string) string {
	name := filepath.Base(devpath)
	return strings.ReplaceAll(name, "!", "/")
}

// FromUevent builds a device from the key=value pairs of a kernel uevent.
func FromUevent(sys string, env map[string]string) (*Device, error) {
	path := env["DEVPATH"]
	if path == "" {
		return nil, fmt.Errorf("uevent without DEVPATH")
	}
	d := New(sys, path)
	d.Action = env["ACTION"]
	for k, v := range env {
		d.Props[k] = v
	}
	d.applyProps()
	if s, err := strconv.ParseUint(env["SEQNUM"], 10, 64); err == nil {
		d.Seqnum = s
	}
	return d, nil
}

// FromSyspath builds a device by reading its uevent file in sysfs.
func FromSyspath(sys, devpath string) (*Device, error) {
	d := New(sys, devpath)
	f, err := os.Open(filepath.Join(sys, devpath, "uevent"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		k, v, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			d.Props[k] = v
		}
	}
	d.Props["DEVPATH"] = devpath
	d.readLinks()
	d.applyProps()
	return d, scanner.Err()
}

func (d *Device) readLinks() {
	if target, err := os.Readlink(filepath.Join(d.Sys, d.DevPath, "subsystem")); err == nil {
		d.Props["SUBSYSTEM"] = filepath.Base(target)
	}
	if target, err := os.Readlink(filepath.Join(d.Sys, d.DevPath, "driver")); err == nil {
		d.Props["DRIVER"] = filepath.Base(target)
	}
}

func (d *Device) applyProps() {
	if d.Props["SUBSYSTEM"] == "" {
		if target, err := os.Readlink(filepath.Join(d.Sys, d.DevPath, "subsystem")); err == nil {
			d.Props["SUBSYSTEM"] = filepath.Base(target)
		}
	}
	d.Subsystem = d.Props["SUBSYSTEM"]
	d.DevType = d.Props["DEVTYPE"]
	d.Driver = d.Props["DRIVER"]
	if d.Driver == "" {
		if target, err := os.Readlink(filepath.Join(d.Sys, d.DevPath, "driver")); err == nil {
			d.Driver = filepath.Base(target)
		}
	}
	if name := d.Props["DEVNAME"]; name != "" {
		if strings.HasPrefix(name, "/dev/") {
			d.DevNode = name
		} else {
			d.DevNode = "/dev/" + name
		}
		d.Props["DEVNAME"] = d.DevNode
	}
	if v, err := strconv.Atoi(d.Props["MAJOR"]); err == nil {
		d.Major = v
	}
	if v, err := strconv.Atoi(d.Props["MINOR"]); err == nil {
		d.Minor = v
	}
	if v, err := strconv.Atoi(d.Props["IFINDEX"]); err == nil {
		d.IfIndex = v
	}
	if d.Action == "" {
		d.Action = d.Props["ACTION"]
	}
	d.Props["DEVPATH"] = d.DevPath
}

// SysPath is the absolute sysfs directory of the device.
func (d *Device) SysPath() string { return filepath.Join(d.Sys, d.DevPath) }

// IsBlock reports whether the device node is a block device.
func (d *Device) IsBlock() bool { return d.Subsystem == "block" }

// ID is the name of the device in the udev database.
func (d *Device) ID() string {
	switch {
	case d.Major >= 0 && d.Minor >= 0 && d.DevNode != "":
		if d.IsBlock() {
			return fmt.Sprintf("b%d:%d", d.Major, d.Minor)
		}
		return fmt.Sprintf("c%d:%d", d.Major, d.Minor)
	case d.IfIndex > 0:
		return fmt.Sprintf("n%d", d.IfIndex)
	default:
		return fmt.Sprintf("+%s:%s", d.Subsystem, strings.ReplaceAll(d.SysName, "/", "!"))
	}
}

// SysNum is the trailing number of the kernel name, as "%n" in rules.
func (d *Device) SysNum() string {
	name := d.SysName
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	return name[i:]
}

// Attr reads a sysfs attribute of the device, trimmed of trailing whitespace.
// Results are cached for the lifetime of the device, as during one event.
func (d *Device) Attr(name string) (string, bool) {
	if v, ok := d.attrs[name]; ok {
		if v == nil {
			return "", false
		}
		return *v, true
	}
	if strings.Contains(name, "..") {
		d.attrs[name] = nil
		return "", false
	}
	path := filepath.Join(d.SysPath(), name)
	info, err := os.Lstat(path)
	if err != nil {
		d.attrs[name] = nil
		return "", false
	}
	var value string
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			d.attrs[name] = nil
			return "", false
		}
		value = filepath.Base(target)
	} else {
		if info.IsDir() || info.Mode().Perm()&0444 == 0 {
			d.attrs[name] = nil
			return "", false
		}
		data, err := readLimited(path, 4096)
		if err != nil {
			d.attrs[name] = nil
			return "", false
		}
		value = strings.TrimRight(string(data), " \t\r\n")
	}
	d.attrs[name] = &value
	return value, true
}

func readLimited(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, err := f.Read(buf)
	if n > 0 {
		return buf[:n], nil
	}
	return nil, err
}

// WriteAttr writes a sysfs attribute of the device.
func (d *Device) WriteAttr(name, value string) error {
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid attribute %q", name)
	}
	delete(d.attrs, name)
	return os.WriteFile(filepath.Join(d.SysPath(), name), []byte(value), 0)
}

// Parent returns the closest ancestor that has a uevent file, or nil.
func (d *Device) Parent() *Device {
	if d.parentSet {
		return d.parent
	}
	d.parentSet = true
	path := d.DevPath
	for {
		path = filepath.Dir(path)
		if path == "/" || path == "." || path == "/devices" || !strings.HasPrefix(path, "/devices") {
			return nil
		}
		if _, err := os.Stat(filepath.Join(d.Sys, path, "uevent")); err != nil {
			continue
		}
		parent, err := FromSyspath(d.Sys, path)
		if err != nil {
			continue
		}
		d.parent = parent
		return parent
	}
}

// Ancestors yields the device itself and then every parent.
func (d *Device) Ancestors() []*Device {
	var out []*Device
	for p := d; p != nil; p = p.Parent() {
		out = append(out, p)
	}
	return out
}

// ParentWith returns the first ancestor (including the device) with the
// given subsystem and, if devtype is not empty, device type.
func (d *Device) ParentWith(subsystem, devtype string) *Device {
	for _, p := range d.Ancestors() {
		if p.Subsystem == subsystem && (devtype == "" || p.DevType == devtype) {
			return p
		}
	}
	return nil
}

// SortedLinks returns the symlinks of the device ordered by name.
func (d *Device) SortedLinks() []string {
	out := make([]string, 0, len(d.Links))
	for l := range d.Links {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// SortedTags returns the tags of the device ordered by name.
func (d *Device) SortedTags() []string {
	out := make([]string, 0, len(d.Tags))
	for t := range d.Tags {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ForeignAttr reads an attribute of another device named as
// "[subsystem/sysname]attribute", as in "[dmi/id]modalias".
func (d *Device) ForeignAttr(spec string) string {
	end := strings.IndexByte(spec, ']')
	if !strings.HasPrefix(spec, "[") || end < 0 {
		return ""
	}
	subsystem, name, ok := strings.Cut(spec[1:end], "/")
	attr := spec[end+1:]
	if !ok || attr == "" || strings.Contains(spec, "..") {
		return ""
	}
	for _, dir := range []string{filepath.Join(d.Sys, "class", subsystem, name), filepath.Join(d.Sys, "bus", subsystem, "devices", name)} {
		data, err := readLimited(filepath.Join(dir, attr), 4096)
		if err == nil {
			return strings.TrimRight(string(data), " \t\r\n")
		}
	}
	return ""
}
