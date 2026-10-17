package builtin

import (
	"math/bits"
	"strconv"
	"strings"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

const (
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03
	evSw  = 0x05

	relX      = 0x00
	relY      = 0x01
	relWheel  = 0x08
	relHwheel = 0x06

	absX        = 0x00
	absY        = 0x01
	absZ        = 0x02
	absRX       = 0x03
	absRY       = 0x04
	absRZ       = 0x05
	absThrottle = 0x06
	absRudder   = 0x07
	absWheel    = 0x08
	absGas      = 0x09
	absBrake    = 0x0a
	absMtSlot   = 0x2f
	absMtPosX   = 0x35
	absMtPosY   = 0x36

	btnMisc         = 0x100
	btn0            = 0x100
	btn1            = 0x101
	btnMouse        = 0x110
	btnLeft         = 0x110
	btnJoystick     = 0x120
	btnTrigger      = 0x120
	btnGamepad      = 0x130
	btnA            = 0x130
	btnDigi         = 0x140
	btnToolPen      = 0x140
	btnToolFinger   = 0x145
	btnTouch        = 0x14a
	btnStylus       = 0x14b
	btnWheel        = 0x150
	keyOK           = 0x160
	btnDpadUp       = 0x220
	btnTriggerHappy = 0x2c0
	keyMax          = 0x2ff

	propPointer       = 0
	propDirect        = 1
	propPointingStick = 5
	propAccelerometer = 6
)

type bitmap []uint64

func (b bitmap) has(bit int) bool {
	w := bit / 64
	return w < len(b) && b[w]&(1<<uint(bit%64)) != 0
}

func (b bitmap) count() int {
	n := 0
	for _, w := range b {
		n += bits.OnesCount64(w)
	}
	return n
}

func parseBitmap(text string) bitmap {
	fields := strings.Fields(text)
	out := make(bitmap, len(fields))
	for i, f := range fields {
		v, err := strconv.ParseUint(f, 16, 64)
		if err != nil {
			continue
		}
		out[len(fields)-1-i] = v
	}
	return out
}

func capability(p *device.Device, name string) bitmap {
	v, _ := p.Attr("capabilities/" + name)
	return parseBitmap(v)
}

func inputID(args []string, d *device.Device) (map[string]string, error) {
	var input *device.Device
	for _, p := range d.Ancestors() {
		if _, ok := p.Attr("capabilities/ev"); ok {
			input = p
			break
		}
	}
	props := map[string]string{"ID_INPUT": "1"}
	if input == nil {
		return props, nil
	}
	ev := capability(input, "ev")
	abs := capability(input, "abs")
	rel := capability(input, "rel")
	key := capability(input, "key")
	inputProps := capability(input, "properties")

	isPointer := testPointers(d, ev, abs, key, rel, inputProps, props)
	isKey := testKey(ev, key, props)
	if !isPointer && !isKey && ev.has(evRel) && (rel.has(relWheel) || rel.has(relHwheel)) {
		props["ID_INPUT_KEY"] = "1"
	}
	if ev.has(evSw) {
		props["ID_INPUT_SWITCH"] = "1"
	}
	return props, nil
}

func testPointers(d *device.Device, ev, abs, key, rel, inputProps bitmap, props map[string]string) bool {
	hasKeys := ev.has(evKey)
	hasAbsCoordinates := abs.has(absX) && abs.has(absY)
	has3DCoordinates := hasAbsCoordinates && abs.has(absZ)
	isAccelerometer := inputProps.has(propAccelerometer)
	if !hasKeys && has3DCoordinates {
		isAccelerometer = true
	}
	if isAccelerometer {
		props["ID_INPUT_ACCELEROMETER"] = "1"
		return true
	}
	isPointingStick := inputProps.has(propPointingStick)
	stylusOrPen := key.has(btnStylus) || key.has(btnToolPen)
	fingerButNoPen := key.has(btnToolFinger) && !key.has(btnToolPen)
	hasMouseButton := false
	for b := btnMouse; b < btnJoystick; b++ {
		if key.has(b) {
			hasMouseButton = true
			break
		}
	}
	hasRelCoordinates := ev.has(evRel) && rel.has(relX) && rel.has(relY)
	hasMtCoordinates := abs.has(absMtPosX) && abs.has(absMtPosY)
	if hasMtCoordinates && abs.has(absMtSlot) && abs.has(absMtSlot-1) {
		hasMtCoordinates = false
	}
	isDirect := inputProps.has(propDirect)
	hasTouch := key.has(btnTouch)
	hasPadButtons := key.has(btn0) && key.has(btn1) && !hasTouch
	hasWheel := false
	if abs.has(absWheel) || key.has(btnWheel) {
		hasWheel = true
	}

	hasJoystickAxesOrButtons := false
	for b := btnJoystick; b < btnDigi; b++ {
		if key.has(b) {
			hasJoystickAxesOrButtons = true
			break
		}
	}
	for b := btnTriggerHappy; !hasJoystickAxesOrButtons && b <= btnTriggerHappy+0x27; b++ {
		if key.has(b) {
			hasJoystickAxesOrButtons = true
		}
	}
	for b := btnDpadUp; !hasJoystickAxesOrButtons && b <= btnDpadUp+3; b++ {
		if key.has(b) {
			hasJoystickAxesOrButtons = true
		}
	}
	for a := absRX; !hasJoystickAxesOrButtons && a <= absBrake; a++ {
		if abs.has(a) {
			hasJoystickAxesOrButtons = true
		}
	}

	if hasAbsCoordinates {
		if stylusOrPen {
			props["ID_INPUT_TABLET"] = "1"
		} else if fingerButNoPen && !isDirect {
			props["ID_INPUT_TOUCHPAD"] = "1"
		} else if hasMouseButton {
			props["ID_INPUT_MOUSE"] = "1"
		} else if hasTouch || isDirect {
			props["ID_INPUT_TOUCHSCREEN"] = "1"
		} else if hasJoystickAxesOrButtons {
			props["ID_INPUT_JOYSTICK"] = "1"
		}
	} else if hasJoystickAxesOrButtons {
		props["ID_INPUT_JOYSTICK"] = "1"
	}
	if hasMtCoordinates {
		if stylusOrPen {
			props["ID_INPUT_TABLET"] = "1"
		} else if fingerButNoPen && !isDirect {
			props["ID_INPUT_TOUCHPAD"] = "1"
		} else if hasTouch || isDirect {
			props["ID_INPUT_TOUCHSCREEN"] = "1"
		}
	}
	if hasPadButtons && hasWheel && !hasRelCoordinates {
		props["ID_INPUT_TABLET"] = "1"
		props["ID_INPUT_TABLET_PAD"] = "1"
	}
	if hasMouseButton && hasRelCoordinates {
		if isPointingStick {
			props["ID_INPUT_POINTINGSTICK"] = "1"
		} else {
			props["ID_INPUT_MOUSE"] = "1"
		}
	}
	if props["ID_INPUT_JOYSTICK"] == "1" && (props["ID_INPUT_TOUCHPAD"] == "1" || props["ID_INPUT_MOUSE"] == "1") {
		delete(props, "ID_INPUT_JOYSTICK")
	}
	for _, k := range []string{"ID_INPUT_TABLET", "ID_INPUT_TOUCHPAD", "ID_INPUT_MOUSE", "ID_INPUT_TOUCHSCREEN", "ID_INPUT_JOYSTICK", "ID_INPUT_POINTINGSTICK", "ID_INPUT_TABLET_PAD"} {
		if props[k] == "1" {
			return true
		}
	}
	return false
}

func testKey(ev, key bitmap, props map[string]string) bool {
	if !ev.has(evKey) {
		return false
	}
	found := false
	for k := 1; k < btnMisc; k++ {
		if key.has(k) {
			found = true
			break
		}
	}
	for k := keyOK; !found && k < btnDpadUp; k++ {
		if key.has(k) {
			found = true
		}
	}
	for k := btnTriggerHappy + 0x28; !found && k <= keyMax; k++ {
		if key.has(k) {
			found = true
		}
	}
	if found {
		props["ID_INPUT_KEY"] = "1"
	}
	if len(key) > 0 && key[0]&0xfffffffe == 0xfffffffe {
		props["ID_INPUT_KEYBOARD"] = "1"
		return true
	}
	return found
}
