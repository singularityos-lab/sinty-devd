// Package uevent receives kernel uevents and forwards processed events to
// libudev clients over the udev netlink group.
package uevent

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	groupKernel = 1
	groupUdev   = 2
	magic       = 0xfeedcafe
	headerSize  = 40
)

// Socket is a NETLINK_KOBJECT_UEVENT socket.
type Socket struct {
	fd int
}

// Listen opens a socket subscribed to kernel uevents.
func Listen() (*Socket, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return nil, err
	}
	unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, 128*1024*1024)
	unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groupKernel}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &Socket{fd: fd}, nil
}

// Sender opens a socket for broadcasting to libudev clients.
func Sender() (*Socket, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &Socket{fd: fd}, nil
}

// Close closes the socket.
func (s *Socket) Close() error { return unix.Close(s.fd) }

// Receive reads one kernel uevent. Messages that do not come from the kernel
// are dropped, since only the kernel may announce devices.
func (s *Socket) Receive() (map[string]string, error) {
	buf := make([]byte, 64*1024)
	oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	for {
		n, oobn, _, from, err := unix.Recvmsg(s.fd, buf, oob, 0)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return nil, err
		}
		nl, ok := from.(*unix.SockaddrNetlink)
		if !ok || nl.Pid != 0 {
			continue
		}
		if !fromRoot(oob[:oobn]) {
			continue
		}
		if env, ok := Parse(buf[:n]); ok {
			return env, nil
		}
	}
}

func fromRoot(oob []byte) bool {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return false
	}
	for _, m := range msgs {
		if cred, err := unix.ParseUnixCredentials(&m); err == nil {
			return cred.Uid == 0
		}
	}
	return false
}

// Parse decodes a kernel uevent: "action@devpath\0KEY=VALUE\0...".
func Parse(msg []byte) (map[string]string, bool) {
	if bytes.HasPrefix(msg, []byte("libudev")) {
		return nil, false
	}
	parts := bytes.Split(msg, []byte{0})
	if len(parts) < 2 || !bytes.Contains(parts[0], []byte("@")) {
		return nil, false
	}
	env := map[string]string{}
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(string(p), "="); ok {
			env[k] = v
		}
	}
	if env["ACTION"] == "" || env["DEVPATH"] == "" {
		return nil, false
	}
	return env, true
}

// Encode builds a libudev monitor message from the final properties of an
// event, including DEVLINKS, TAGS and USEC_INITIALIZED.
func Encode(props map[string]string, subsystem, devtype string, tags []string) []byte {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var body bytes.Buffer
	for _, k := range keys {
		body.WriteString(k)
		body.WriteByte('=')
		body.WriteString(props[k])
		body.WriteByte(0)
	}
	var bloom uint64
	for _, t := range tags {
		bloom |= tagBloom(t)
	}
	header := make([]byte, headerSize)
	copy(header, "libudev\x00")
	binary.BigEndian.PutUint32(header[8:], magic)
	binary.LittleEndian.PutUint32(header[12:], headerSize)
	binary.LittleEndian.PutUint32(header[16:], headerSize)
	binary.LittleEndian.PutUint32(header[20:], uint32(body.Len()))
	if subsystem != "" {
		binary.BigEndian.PutUint32(header[24:], murmur2([]byte(subsystem), 0))
	}
	if devtype != "" {
		binary.BigEndian.PutUint32(header[28:], murmur2([]byte(devtype), 0))
	}
	binary.BigEndian.PutUint32(header[32:], uint32(bloom>>32))
	binary.BigEndian.PutUint32(header[36:], uint32(bloom))
	return append(header, body.Bytes()...)
}

// Broadcast sends an encoded message to every libudev monitor.
func (s *Socket) Broadcast(msg []byte) error {
	err := unix.Sendto(s.fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groupUdev})
	if err == unix.ECONNREFUSED || err == unix.ESRCH {
		return nil
	}
	return err
}

func tagBloom(tag string) uint64 {
	h := murmur2([]byte(tag), 0)
	var bits uint64
	bits |= 1 << (h & 63)
	bits |= 1 << ((h >> 6) & 63)
	bits |= 1 << ((h >> 12) & 63)
	bits |= 1 << ((h >> 18) & 63)
	return bits
}

func murmur2(key []byte, seed uint32) uint32 {
	const m = 0x5bd1e995
	const r = 24
	h := seed ^ uint32(len(key))
	for len(key) >= 4 {
		k := binary.LittleEndian.Uint32(key)
		k *= m
		k ^= k >> r
		k *= m
		h *= m
		h ^= k
		key = key[4:]
	}
	switch len(key) {
	case 3:
		h ^= uint32(key[2]) << 16
		fallthrough
	case 2:
		h ^= uint32(key[1]) << 8
		fallthrough
	case 1:
		h ^= uint32(key[0])
		h *= m
	}
	h ^= h >> 13
	h *= m
	h ^= h >> 15
	return h
}

// String formats a uevent for logs.
func String(env map[string]string) string {
	return fmt.Sprintf("%s %s", env["ACTION"], env["DEVPATH"])
}
