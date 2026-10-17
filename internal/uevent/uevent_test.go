package uevent

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestParse(t *testing.T) {
	env, ok := Parse([]byte("add@/devices/x\x00ACTION=add\x00DEVPATH=/devices/x\x00SUBSYSTEM=input\x00SEQNUM=7\x00"))
	if !ok || env["SUBSYSTEM"] != "input" || env["SEQNUM"] != "7" {
		t.Fatalf("parse: %v %v", ok, env)
	}
	if _, ok := Parse([]byte("libudev\x00rest")); ok {
		t.Fatal("libudev messages must not parse as kernel events")
	}
}

func TestEncode(t *testing.T) {
	msg := Encode(map[string]string{"ACTION": "add", "SUBSYSTEM": "input"}, "input", "", []string{"seat"})
	if !bytes.HasPrefix(msg, []byte("libudev\x00")) {
		t.Fatal("missing prefix")
	}
	if binary.BigEndian.Uint32(msg[8:]) != 0xfeedcafe {
		t.Fatal("bad magic")
	}
	off := binary.LittleEndian.Uint32(msg[16:])
	n := binary.LittleEndian.Uint32(msg[20:])
	if string(msg[off:off+n]) != "ACTION=add\x00SUBSYSTEM=input\x00" {
		t.Fatalf("body %q", msg[off:off+n])
	}
	if binary.BigEndian.Uint32(msg[24:]) != murmur2([]byte("input"), 0) {
		t.Fatal("subsystem hash")
	}
	if msg[32] == 0 && msg[33] == 0 && msg[34] == 0 && msg[35] == 0 && msg[36] == 0 && msg[37] == 0 && msg[38] == 0 && msg[39] == 0 {
		t.Fatal("tag bloom is empty")
	}
}

func TestMurmur2(t *testing.T) {
	if murmur2([]byte(""), 0) != 0 {
		t.Fatal("empty input")
	}
	if murmur2([]byte("input"), 0) == murmur2([]byte("block"), 0) {
		t.Fatal("collision")
	}
}
