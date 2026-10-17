package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

func usbClassType(class string) string {
	switch class {
	case "01":
		return "audio"
	case "03":
		return "hid"
	case "05":
		return "Physical"
	case "06":
		return "media"
	case "07":
		return "printer"
	case "08":
		return "generic"
	case "09":
		return "hub"
	case "0a":
		return "cdc_data"
	case "0b":
		return "smartcard"
	case "0e":
		return "video"
	case "e0":
		return "wireless"
	default:
		return "generic"
	}
}

func usbStorageType(subclass string) string {
	switch subclass {
	case "02", "05":
		return "cd"
	case "04":
		return "floppy"
	case "06":
		return "disk"
	default:
		return "generic"
	}
}

func usbID(args []string, d *device.Device) (map[string]string, error) {
	dev := d.ParentWith("usb", "usb_device")
	if dev == nil {
		return nil, fmt.Errorf("not a usb device")
	}
	iface := d.ParentWith("usb", "usb_interface")
	props := map[string]string{}
	typ := ""
	if iface != nil {
		class, _ := iface.Attr("bInterfaceClass")
		subclass, _ := iface.Attr("bInterfaceSubClass")
		if class == "08" {
			typ = usbStorageType(subclass)
		} else {
			typ = usbClassType(class)
		}
		if num, ok := iface.Attr("bInterfaceNumber"); ok {
			props["ID_USB_INTERFACE_NUM"] = num
		}
		if drv := iface.Driver; drv != "" {
			props["ID_USB_DRIVER"] = drv
		}
	}
	vendorID, _ := dev.Attr("idVendor")
	productID, _ := dev.Attr("idProduct")
	vendor, _ := dev.Attr("manufacturer")
	if vendor == "" {
		vendor = vendorID
	}
	model, _ := dev.Attr("product")
	if model == "" {
		model = productID
	}
	revision, _ := dev.Attr("bcdDevice")
	serial, _ := dev.Attr("serial")
	props["ID_VENDOR"] = replaceChars(replaceWhitespace(vendor))
	props["ID_VENDOR_ENC"] = encodeString(vendor)
	props["ID_VENDOR_ID"] = vendorID
	props["ID_MODEL"] = replaceChars(replaceWhitespace(model))
	props["ID_MODEL_ENC"] = encodeString(model)
	props["ID_MODEL_ID"] = productID
	props["ID_REVISION"] = revision
	full := props["ID_VENDOR"] + "_" + props["ID_MODEL"]
	if serial != "" {
		cleaned := replaceChars(replaceWhitespace(serial))
		full += "_" + cleaned
		props["ID_SERIAL_SHORT"] = cleaned
	}
	props["ID_SERIAL"] = full
	if typ != "" {
		props["ID_TYPE"] = typ
	}
	props["ID_BUS"] = "usb"
	props["ID_USB_INTERFACES"] = usbInterfaces(dev)
	for _, k := range []string{"ID_VENDOR", "ID_VENDOR_ENC", "ID_VENDOR_ID", "ID_MODEL", "ID_MODEL_ENC", "ID_MODEL_ID", "ID_REVISION", "ID_SERIAL", "ID_SERIAL_SHORT", "ID_TYPE"} {
		if v, ok := props[k]; ok {
			props["ID_USB_"+strings.TrimPrefix(k, "ID_")] = v
		}
	}
	return props, nil
}

func usbInterfaces(dev *device.Device) string {
	entries, err := os.ReadDir(dev.SysPath())
	if err != nil {
		return ""
	}
	var parts []string
	for _, e := range entries {
		dir := filepath.Join(dev.SysPath(), e.Name())
		class := readTrim(filepath.Join(dir, "bInterfaceClass"))
		if class == "" {
			continue
		}
		sub := readTrim(filepath.Join(dir, "bInterfaceSubClass"))
		proto := readTrim(filepath.Join(dir, "bInterfaceProtocol"))
		entry := class + sub + proto
		dup := false
		for _, p := range parts {
			if p == entry {
				dup = true
			}
		}
		if !dup {
			parts = append(parts, entry)
		}
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return ""
	}
	return ":" + strings.Join(parts, ":") + ":"
}

func pathID(args []string, d *device.Device) (map[string]string, error) {
	var parts, revisionParts []string
	usbRevision := ""
	prepend := func(s string) {
		parts = append([]string{s}, parts...)
		revisionParts = append([]string{s}, revisionParts...)
	}
	supported := false
	ancestors := d.Ancestors()
	for i := 0; i < len(ancestors); i++ {
		p := ancestors[i]
		switch p.Subsystem {
		case "pci":
			prepend("pci-" + p.SysName)
			supported = true
			for i+1 < len(ancestors) && ancestors[i+1].Subsystem == "pci" {
				i++
			}
		case "usb":
			if p.DevType == "usb_interface" {
				port := p.SysName
				if j := strings.IndexByte(port, '-'); j >= 0 {
					port = port[j+1:]
				}
				parts = append([]string{"usb-0:" + port}, parts...)
				if dev := p.Parent(); dev != nil {
					if v, ok := dev.Attr("version"); ok {
						if major, _, _ := strings.Cut(strings.TrimSpace(v), "."); major != "" {
							usbRevision = major
						}
					}
				}
				revisionParts = append([]string{"usbv" + usbRevision + "-0:" + port}, revisionParts...)
				supported = true
				for i+1 < len(ancestors) && ancestors[i+1].Subsystem == "usb" {
					i++
				}
			}
		case "platform":
			prepend("platform-" + p.SysName)
			supported = true
			for i+1 < len(ancestors) && ancestors[i+1].Subsystem == "platform" {
				i++
			}
		case "acpi":
			prepend("acpi-" + p.SysName)
			supported = true
			for i+1 < len(ancestors) && ancestors[i+1].Subsystem == "acpi" {
				i++
			}
		case "serio":
			prepend("serio-" + p.SysNum())
		case "nvme":
			if d.Subsystem == "block" && i == 1 || (i == 2 && d.DevType == "partition") {
				if nsid := nvmeNamespace(d); nsid != "" {
					prepend("nvme-" + nsid)
				}
			}
			supported = true
		case "scsi":
			if p.DevType == "scsi_device" {
				if seg := scsiSegment(p); seg != "" {
					prepend(seg)
					supported = true
				}
				for i+1 < len(ancestors) && (ancestors[i+1].Subsystem == "scsi" || ancestors[i+1].Subsystem == "") {
					i++
				}
			}
		case "virtio":
			supported = true
		case "xen":
			prepend("xen-" + p.SysName)
			supported = true
		}
	}
	if len(parts) == 0 || !supported {
		return nil, fmt.Errorf("no path")
	}
	path := strings.Join(parts, "-")
	props := map[string]string{"ID_PATH": path, "ID_PATH_TAG": pathTag(path)}
	if usbRevision != "" {
		withRevision := strings.Join(revisionParts, "-")
		props["ID_PATH_WITH_USB_REVISION"] = withRevision
	}
	return props, nil
}

func pathTag(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func nvmeNamespace(d *device.Device) string {
	disk := d
	if d.DevType == "partition" {
		disk = d.Parent()
	}
	if disk == nil {
		return ""
	}
	if nsid, ok := disk.Attr("nsid"); ok {
		return nsid
	}
	return ""
}

func scsiSegment(p *device.Device) string {
	for _, a := range p.Ancestors() {
		if strings.HasPrefix(a.SysName, "ata") && a.Subsystem == "" {
			if port := readTrim(filepath.Join(a.SysPath(), "ata_port", a.SysName, "port_no")); port != "" {
				return "ata-" + port
			}
			return "ata-" + strings.TrimPrefix(a.SysName, "ata")
		}
		if a.Subsystem == "ata_port" {
			if port, ok := a.Attr("port_no"); ok {
				return "ata-" + port
			}
		}
	}
	fields := strings.Split(p.SysName, ":")
	if len(fields) == 4 {
		return "scsi-" + strings.Join(fields[1:], ":")
	}
	return ""
}
