//go:build windows

package win

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Control of Amyuni's "USB Mobile Monitor Virtual Display" driver
// (usbmmidd v2), installed by deploy/windows/virtual-display.ps1. A virtual
// monitor gives the desktop compositor an active display while a laptop's
// lid is closed, so Desktop Duplication delivers frames
// (docs/remote-desktop-design.md 5.11).

var (
	procEnumDisplayDevicesW       = user32.NewProc("EnumDisplayDevicesW")
	procEnumDisplaySettingsW      = user32.NewProc("EnumDisplaySettingsW")
	procChangeDisplaySettingsExW  = user32.NewProc("ChangeDisplaySettingsExW")
	procGetDisplayConfigBufSizes  = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig        = user32.NewProc("QueryDisplayConfig")
	procDisplayConfigGetDeviceInf = user32.NewProc("DisplayConfigGetDeviceInfo")
	procDisplayConfigSetDeviceInf = user32.NewProc("DisplayConfigSetDeviceInfo")

	// The driver's device interface and the control code that adds or
	// removes one monitor, as used by the vendor's deviceinstaller.
	guidVirtualDisplay = windows.GUID{Data1: 0xb5ffd75f, Data2: 0xda40, Data3: 0x4353, Data4: [8]byte{0x8f, 0xf8, 0xb6, 0xda, 0xf6, 0xf1, 0xd8, 0xca}}
)

const (
	ioctlVirtualDisplayPlug = 0x23340c
	virtualDisplayAdapter   = "USB Mobile Monitor Virtual Display"

	displayDeviceAttached = 0x1

	dmPelsWidth  = 0x80000
	dmPelsHeight = 0x100000

	cdsUpdateRegistry      = 0x1
	enumCurrentSettings    = ^uintptr(0) // ENUM_CURRENT_SETTINGS (-1)
	qdcOnlyActivePaths     = 0x2
	displayConfigPathSize  = 72
	displayConfigModeSize  = 64
	displayConfigGetSource = 1
	// Undocumented, but what the Settings app itself uses for per-monitor
	// scaling: the scale is an index relative to the recommended one.
	displayConfigGetDPIScale = -3
	displayConfigSetDPIScale = -4
)

// dpiSteps are the scale factors Windows offers, in percent.
var dpiSteps = []int{100, 125, 150, 175, 200, 225, 250, 300, 350, 400, 450, 500}

type displayDevice struct {
	Size         uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

// devMode is DEVMODEW with the display members of its unions (220 bytes).
type devMode struct {
	DeviceName         [32]uint16
	SpecVersion        uint16
	DriverVersion      uint16
	Size               uint16
	DriverExtra        uint16
	Fields             uint32
	PositionX          int32
	PositionY          int32
	DisplayOrientation uint32
	DisplayFixedOutput uint32
	Color              int16
	Duplex             int16
	YResolution        int16
	TTOption           int16
	Collate            int16
	FormName           [32]uint16
	LogPixels          uint16
	BitsPerPel         uint32
	PelsWidth          uint32
	PelsHeight         uint32
	DisplayFlags       uint32
	DisplayFrequency   uint32
	ICMMethod          uint32
	ICMIntent          uint32
	MediaType          uint32
	DitherType         uint32
	Reserved1          uint32
	Reserved2          uint32
	PanningWidth       uint32
	PanningHeight      uint32
}

// VirtualDisplayInstalled reports whether the driver is present.
func VirtualDisplayInstalled() bool {
	paths, err := windows.CM_Get_Device_Interface_List("", &guidVirtualDisplay, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	return err == nil && len(paths) > 0
}

// virtualDisplayControl asks the driver to add or remove one monitor.
func virtualDisplayControl(add bool) error {
	paths, err := windows.CM_Get_Device_Interface_List("", &guidVirtualDisplay, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("the virtual display driver is not installed")
	}
	name, err := windows.UTF16PtrFromString(paths[0])
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return fmt.Errorf("open the virtual display driver: %w", err)
	}
	defer windows.CloseHandle(h)
	cmd := [4]byte{}
	if add {
		cmd[0] = 0x10
	}
	var n uint32
	if err := windows.DeviceIoControl(h, ioctlVirtualDisplayPlug, &cmd[0], uint32(len(cmd)), nil, 0, &n, nil); err != nil {
		return fmt.Errorf("virtual display control: %w", err)
	}
	return nil
}

const displayDeviceActive = 0x1 // DISPLAY_DEVICE_ACTIVE on a monitor

// monitorPresent reports whether a monitor is connected to the adapter
// output named device. The adapter's own "attached to desktop" flag is not
// enough: Windows leaves it set after the last monitor has gone, because a
// desktop always has a primary display.
func monitorPresent(device *uint16) bool {
	mon := displayDevice{}
	mon.Size = uint32(unsafe.Sizeof(mon))
	r, _ := Call(procEnumDisplayDevicesW, uintptr(unsafe.Pointer(device)), 0, uintptr(unsafe.Pointer(&mon)), 0)
	return r != 0 && mon.StateFlags&displayDeviceActive != 0
}

// VirtualDisplays returns the device names (\\.\DISPLAYn) of the driver's
// outputs that have a virtual monitor connected.
func VirtualDisplays() []string {
	var out []string
	for i := 0; ; i++ {
		dd := displayDevice{}
		dd.Size = uint32(unsafe.Sizeof(dd))
		if r, _ := Call(procEnumDisplayDevicesW, 0, uintptr(i), uintptr(unsafe.Pointer(&dd)), 0); r == 0 {
			return out
		}
		if dd.StateFlags&displayDeviceAttached != 0 && windows.UTF16ToString(dd.DeviceString[:]) == virtualDisplayAdapter &&
			monitorPresent(&dd.DeviceName[0]) {
			out = append(out, windows.UTF16ToString(dd.DeviceName[:]))
		}
	}
}

// PlugVirtualDisplay adds one virtual monitor and returns its device name
// once it is part of the desktop.
func PlugVirtualDisplay(timeout time.Duration) (string, error) {
	before := map[string]bool{}
	for _, n := range VirtualDisplays() {
		before[n] = true
	}
	if err := virtualDisplayControl(true); err != nil {
		return "", err
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		for _, n := range VirtualDisplays() {
			if !before[n] {
				return n, nil
			}
		}
	}
	return "", errors.New("the virtual monitor did not appear")
}

// UnplugVirtualDisplay removes one virtual monitor.
func UnplugVirtualDisplay() error { return virtualDisplayControl(false) }

// SetDisplayMode switches the display device to w x h, keeping its other
// settings, and remembers the mode for the next time it is connected.
func SetDisplayMode(device string, w, h int) error {
	name, err := windows.UTF16PtrFromString(device)
	if err != nil {
		return err
	}
	dm := devMode{}
	dm.Size = uint16(unsafe.Sizeof(dm))
	if r, _ := Call(procEnumDisplaySettingsW, uintptr(unsafe.Pointer(name)), enumCurrentSettings, uintptr(unsafe.Pointer(&dm))); r == 0 {
		return fmt.Errorf("read the display mode of %s", device)
	}
	if int(dm.PelsWidth) == w && int(dm.PelsHeight) == h {
		return nil
	}
	dm.PelsWidth, dm.PelsHeight = uint32(w), uint32(h)
	dm.Fields = dmPelsWidth | dmPelsHeight
	// DISP_CHANGE_SUCCESSFUL is 0.
	if r, _ := Call(procChangeDisplaySettingsExW, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&dm)), 0, cdsUpdateRegistry, 0); int32(r) != 0 {
		return fmt.Errorf("set %s to %dx%d: ChangeDisplaySettingsEx returned %d", device, w, h, int32(r))
	}
	return nil
}

type displayConfigHeader struct {
	Type      int32
	Size      uint32
	AdapterID windows.LUID
	ID        uint32
}

// displaySource finds the display-configuration source of a device name.
func displaySource(device string) (windows.LUID, uint32, error) {
	var numPaths, numModes uint32
	if r, _ := Call(procGetDisplayConfigBufSizes, qdcOnlyActivePaths, uintptr(unsafe.Pointer(&numPaths)), uintptr(unsafe.Pointer(&numModes))); r != 0 {
		return windows.LUID{}, 0, fmt.Errorf("GetDisplayConfigBufferSizes: %d", r)
	}
	if numPaths == 0 {
		return windows.LUID{}, 0, errors.New("no active display paths")
	}
	paths := make([]byte, int(numPaths)*displayConfigPathSize)
	modes := make([]byte, max(int(numModes), 1)*displayConfigModeSize)
	if r, _ := Call(procQueryDisplayConfig, qdcOnlyActivePaths, uintptr(unsafe.Pointer(&numPaths)), uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&numModes)), uintptr(unsafe.Pointer(&modes[0])), 0); r != 0 {
		return windows.LUID{}, 0, fmt.Errorf("QueryDisplayConfig: %d", r)
	}
	for i := 0; i < int(numPaths); i++ {
		// DISPLAYCONFIG_PATH_INFO begins with the source's adapter and id.
		p := paths[i*displayConfigPathSize:]
		adapter := *(*windows.LUID)(unsafe.Pointer(&p[0]))
		id := *(*uint32)(unsafe.Pointer(&p[8]))
		var src struct {
			Header displayConfigHeader
			Name   [32]uint16
		}
		src.Header = displayConfigHeader{Type: displayConfigGetSource, Size: uint32(unsafe.Sizeof(src)), AdapterID: adapter, ID: id}
		if r, _ := Call(procDisplayConfigGetDeviceInf, uintptr(unsafe.Pointer(&src))); r != 0 {
			continue
		}
		if windows.UTF16ToString(src.Name[:]) == device {
			return adapter, id, nil
		}
	}
	return windows.LUID{}, 0, fmt.Errorf("%s is not an active display", device)
}

// SetDisplayScale sets the display's scale factor (1.5 for 150 %) to the
// nearest one Windows offers for it.
func SetDisplayScale(device string, scale float64) error {
	if err := Find(procGetDisplayConfigBufSizes, procQueryDisplayConfig, procDisplayConfigGetDeviceInf, procDisplayConfigSetDeviceInf); err != nil {
		return err
	}
	adapter, id, err := displaySource(device)
	if err != nil {
		return err
	}
	var get struct {
		Header        displayConfigHeader
		Min, Cur, Max int32
	}
	get.Header = displayConfigHeader{Type: displayConfigGetDPIScale, Size: uint32(unsafe.Sizeof(get)), AdapterID: adapter, ID: id}
	if r, _ := Call(procDisplayConfigGetDeviceInf, uintptr(unsafe.Pointer(&get))); r != 0 {
		return fmt.Errorf("read the scale of %s: %d", device, r)
	}
	// Min is how many steps below the recommended scale 100 % lies.
	recommended := int(-get.Min)
	want := 0
	for i, pct := range dpiSteps {
		if d, best := float64(pct)-scale*100, float64(dpiSteps[want])-scale*100; d*d < best*best {
			want = i
		}
	}
	rel := int32(min(max(want-recommended, int(get.Min)), int(get.Max)))
	if rel == get.Cur {
		return nil
	}
	var set struct {
		Header displayConfigHeader
		Scale  int32
	}
	set.Header = displayConfigHeader{Type: displayConfigSetDPIScale, Size: uint32(unsafe.Sizeof(set)), AdapterID: adapter, ID: id}
	set.Scale = rel
	if r, _ := Call(procDisplayConfigSetDeviceInf, uintptr(unsafe.Pointer(&set))); r != 0 {
		return fmt.Errorf("set the scale of %s: %d", device, r)
	}
	return nil
}
