// Package db writes the udev database that libudev reads.
package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

// Root is the runtime directory of the database.
var Root = "/run/udev"

var kernelKeys = map[string]bool{
	"ACTION": true, "DEVPATH": true, "SUBSYSTEM": true, "SEQNUM": true, "DEVNAME": true,
	"DEVTYPE": true, "MAJOR": true, "MINOR": true, "DRIVER": true, "IFINDEX": true,
	"INTERFACE": true, "MODALIAS": true, "DEVMODE": true, "DEVUID": true, "DEVGID": true,
	"SYNTH_UUID": true, "PRODUCT": true, "TYPE": true, "NAME": true, "PHYS": true, "UNIQ": true,
	"PROP": true, "EV": true, "KEY": true, "REL": true, "ABS": true, "MSC": true, "SW": true,
	"LED": true, "SND": true, "FF": true, "BUSNUM": true, "DEVNUM": true, "PCI_CLASS": true,
	"PCI_ID": true, "PCI_SUBSYS_ID": true, "PCI_SLOT_NAME": true, "DISKSEQ": true, "PARTN": true,
	"PARTNAME": true, "HID_ID": true, "HID_NAME": true, "HID_PHYS": true, "HID_UNIQ": true,
}

// IsKernelKey reports whether a property comes from the kernel uevent rather
// than from rules, so it is not repeated in the database.
func IsKernelKey(k string) bool { return kernelKeys[k] }

// Write stores the device in the database, records its tags and returns the
// initialization time it used.
func Write(d *device.Device, initialized int64, linkPriority int) error {
	dir := filepath.Join(Root, "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, l := range d.SortedLinks() {
		fmt.Fprintf(&b, "S:%s\n", strings.TrimPrefix(l, "/dev/"))
	}
	if linkPriority != 0 {
		fmt.Fprintf(&b, "L:%d\n", linkPriority)
	}
	fmt.Fprintf(&b, "I:%d\n", initialized)
	keys := make([]string, 0, len(d.Props))
	for k := range d.Props {
		if !IsKernelKey(k) && k != "DEVLINKS" && k != "TAGS" && k != "CURRENT_TAGS" && k != "USEC_INITIALIZED" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "E:%s=%s\n", k, d.Props[k])
	}
	for _, t := range d.SortedTags() {
		fmt.Fprintf(&b, "G:%s\n", t)
	}
	for _, t := range d.SortedTags() {
		fmt.Fprintf(&b, "Q:%s\n", t)
	}
	b.WriteString("V:1\n")
	target := filepath.Join(dir, d.ID())
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	return writeTags(d)
}

func writeTags(d *device.Device) error {
	id := d.ID()
	for t := range d.Tags {
		dir := filepath.Join(Root, "tags", t)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, id), os.O_CREATE|os.O_WRONLY, 0o444)
		if err != nil {
			return err
		}
		f.Close()
	}
	return nil
}

// Read returns the database entry of a device: properties, tags, links and
// the initialization time, or ok=false when there is none.
func Read(id string) (props map[string]string, tags []string, links []string, initialized int64, ok bool) {
	data, err := os.ReadFile(filepath.Join(Root, "data", id))
	if err != nil {
		return nil, nil, nil, 0, false
	}
	props = map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		v := line[2:]
		switch line[0] {
		case 'E':
			if k, val, found := strings.Cut(v, "="); found {
				props[k] = val
			}
		case 'G':
			tags = append(tags, v)
		case 'S':
			links = append(links, "/dev/"+v)
		case 'I':
			initialized, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return props, tags, links, initialized, true
}

// Remove deletes the database entry and tags of a device.
func Remove(d *device.Device) {
	id := d.ID()
	os.Remove(filepath.Join(Root, "data", id))
	entries, _ := os.ReadDir(filepath.Join(Root, "tags"))
	for _, e := range entries {
		os.Remove(filepath.Join(Root, "tags", e.Name(), id))
	}
}
