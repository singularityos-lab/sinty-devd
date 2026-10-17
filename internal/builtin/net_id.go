package builtin

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

func netPrefix(d *device.Device) string {
	typ, _ := d.Attr("type")
	switch typ {
	case "32":
		return "ib"
	case "256":
		return "sl"
	case "1":
	default:
		return ""
	}
	switch d.DevType {
	case "wlan":
		return "wl"
	case "wwan":
		return "ww"
	}
	return "en"
}

func netID(args []string, d *device.Device) (map[string]string, error) {
	prefix := netPrefix(d)
	if prefix == "" {
		return nil, fmt.Errorf("unsupported link type")
	}
	props := map[string]string{}
	mac := ""
	if assign, _ := d.Attr("addr_assign_type"); assign == "0" {
		mac, _ = d.Attr("address")
	} else {
		mac = permanentAddress(d.SysName)
	}
	if mac != "" && mac != "00:00:00:00:00:00" {
		props["ID_NET_NAME_MAC"] = prefix + "x" + strings.ReplaceAll(mac, ":", "")
	}
	pci := d.ParentWith("pci", "")
	if pci == nil {
		if len(props) == 0 {
			return nil, fmt.Errorf("no stable name")
		}
		return props, nil
	}
	var domain, bus, slot, function int
	if _, err := fmt.Sscanf(pci.SysName, "%x:%x:%x.%d", &domain, &bus, &slot, &function); err != nil {
		return props, nil
	}
	devPort := 0
	if v, ok := d.Attr("dev_port"); ok {
		devPort, _ = strconv.Atoi(v)
	}
	if devPort == 0 {
		if v, ok := d.Attr("dev_id"); ok {
			if n, err := strconv.ParseInt(strings.TrimPrefix(v, "0x"), 16, 32); err == nil {
				devPort = int(n)
			}
		}
	}
	suffix := ""
	if function > 0 || multifunction(pci) {
		suffix += fmt.Sprintf("f%d", function)
	}
	if devPort > 0 {
		suffix += fmt.Sprintf("d%d", devPort)
	}
	domainPrefix := ""
	if domain > 0 {
		domainPrefix = fmt.Sprintf("P%d", domain)
	}
	pciPath := fmt.Sprintf("%sp%ds%d%s", domainPrefix, bus, slot, suffix)

	usbPart := ""
	if usb := d.ParentWith("usb", "usb_interface"); usb != nil {
		name := usb.SysName
		if i := strings.IndexByte(name, '-'); i >= 0 {
			name = name[i+1:]
		}
		ports, config, iface := name, "", ""
		if i := strings.IndexByte(name, ':'); i >= 0 {
			ports = name[:i]
			ci := name[i+1:]
			config, iface, _ = strings.Cut(ci, ".")
		}
		usbPart = "u" + strings.ReplaceAll(ports, ".", "u")
		if config != "" && config != "1" {
			usbPart += "c" + config
		}
		if iface != "" && iface != "0" {
			usbPart += "i" + iface
		}
	}
	props["ID_NET_NAME_PATH"] = prefix + pciPath + usbPart

	if usbPart == "" {
		if idx, ok := pci.Attr("acpi_index"); ok {
			if n, err := strconv.Atoi(idx); err == nil && n > 0 && n <= 16383 {
				props["ID_NET_NAME_ONBOARD"] = fmt.Sprintf("%so%d%s", prefix, n, portSuffix(devPort))
			}
		} else if idx, ok := pci.Attr("index"); ok {
			if n, err := strconv.Atoi(idx); err == nil && n > 0 && n <= 16383 {
				props["ID_NET_NAME_ONBOARD"] = fmt.Sprintf("%so%d%s", prefix, n, portSuffix(devPort))
			}
		}
		if label, ok := pci.Attr("label"); ok && label != "" {
			props["ID_NET_LABEL_ONBOARD"] = prefix + " " + label
		}
		if s := hotplugSlot(d.Sys, domain, bus, slot); s > 0 {
			props["ID_NET_NAME_SLOT"] = fmt.Sprintf("%s%ss%d%s", prefix, domainPrefix, s, suffix)
		}
	}
	return props, nil
}

func portSuffix(port int) string {
	if port > 0 {
		return fmt.Sprintf("d%d", port)
	}
	return ""
}

func multifunction(pci *device.Device) bool {
	parent := filepath.Dir(pci.SysPath())
	base := pci.SysName
	dot := strings.LastIndexByte(base, '.')
	if dot < 0 {
		return false
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return false
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base[:dot+1]) {
			count++
		}
	}
	return count > 1
}

func hotplugSlot(sys string, domain, bus, slot int) int {
	entries, err := os.ReadDir(filepath.Join(sys, "bus/pci/slots"))
	if err != nil {
		return 0
	}
	want := fmt.Sprintf("%04x:%02x:%02x", domain, bus, slot)
	for _, e := range entries {
		address := readTrim(filepath.Join(sys, "bus/pci/slots", e.Name(), "address"))
		if address == want {
			if n, err := strconv.Atoi(e.Name()); err == nil {
				return n
			}
		}
	}
	return 0
}

func permanentAddress(ifname string) string {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	defer unix.Close(fd)
	var perm struct {
		cmd  uint32
		size uint32
		data [32]byte
	}
	perm.cmd = 0x20
	perm.size = 32
	var req struct {
		name [unix.IFNAMSIZ]byte
		data uintptr
		pad  [16]byte
	}
	copy(req.name[:], ifname)
	req.data = uintptr(unsafe.Pointer(&perm))
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCETHTOOL, uintptr(unsafe.Pointer(&req))); errno != 0 {
		return ""
	}
	if perm.size != 6 {
		return ""
	}
	parts := make([]string, 6)
	zero := true
	for i := 0; i < 6; i++ {
		parts[i] = fmt.Sprintf("%02x", perm.data[i])
		if perm.data[i] != 0 {
			zero = false
		}
	}
	if zero {
		return ""
	}
	return strings.Join(parts, ":")
}
