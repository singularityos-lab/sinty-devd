package builtin

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

const (
	eviocsKeycode = 0x40084504
)

func eviocsAbs(axis uint) uint {
	return 0x401845c0 + axis
}

type absInfo struct {
	Value      int32
	Minimum    int32
	Maximum    int32
	Fuzz       int32
	Flat       int32
	Resolution int32
}

func keyboard(args []string, d *device.Device) (map[string]string, error) {
	if d.DevNode == "" {
		return nil, nil
	}
	var keys [][2]uint32
	var axes []string
	for k, v := range d.Props {
		switch {
		case strings.HasPrefix(k, "KEYBOARD_KEY_"):
			scancode, err := strconv.ParseUint(strings.TrimPrefix(k, "KEYBOARD_KEY_"), 16, 32)
			if err != nil {
				continue
			}
			name := strings.TrimPrefix(strings.ToLower(v), "!")
			code, ok := keyCodes[name]
			if !ok {
				if n, err := strconv.ParseUint(name, 0, 32); err == nil {
					code, ok = uint32(n), true
				}
			}
			if ok {
				keys = append(keys, [2]uint32{uint32(scancode), code})
			}
		case strings.HasPrefix(k, "EVDEV_ABS_"):
			axes = append(axes, k)
		}
	}
	if len(keys) == 0 && len(axes) == 0 {
		return nil, nil
	}
	f, err := os.OpenFile(d.DevNode, os.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fd := f.Fd()
	for _, kv := range keys {
		pair := kv
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, eviocsKeycode, uintptr(unsafe.Pointer(&pair))); errno != 0 {
			return nil, fmt.Errorf("remapping scancode %#x: %v", kv[0], errno)
		}
	}
	for _, k := range axes {
		axis, err := strconv.ParseUint(strings.TrimPrefix(k, "EVDEV_ABS_"), 16, 8)
		if err != nil {
			continue
		}
		var info absInfo
		get := 0x80184540 + uintptr(axis)
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, get, uintptr(unsafe.Pointer(&info))); errno != 0 {
			continue
		}
		fields := strings.Split(d.Props[k], ":")
		set := func(i int, dst *int32) {
			if i < len(fields) && fields[i] != "" {
				if n, err := strconv.Atoi(fields[i]); err == nil {
					*dst = int32(n)
				}
			}
		}
		set(0, &info.Minimum)
		set(1, &info.Maximum)
		set(2, &info.Resolution)
		set(3, &info.Fuzz)
		set(4, &info.Flat)
		unix.Syscall(unix.SYS_IOCTL, fd, uintptr(eviocsAbs(uint(axis))), uintptr(unsafe.Pointer(&info)))
	}
	return nil, nil
}

func btrfsReady(node string) (bool, error) {
	f, err := os.OpenFile("/dev/btrfs-control", os.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var args [4096]byte
	copy(args[8:], node)
	const btrfsIocDevicesReady = 0x90009427
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), btrfsIocDevicesReady, uintptr(unsafe.Pointer(&args[0])))
	if errno != 0 {
		return false, errno
	}
	return r == 0, nil
}
