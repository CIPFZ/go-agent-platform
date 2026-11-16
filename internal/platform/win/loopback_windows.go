//go:build windows

package win

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// System audio capture through WASAPI loopback (Audioclient.h,
// Mmdeviceapi.h), called through COM vtables like the UI Automation client.
// It records what the default playback device is playing.

var (
	procCoTaskMemFree = ole32.NewProc("CoTaskMemFree")

	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xbcde0395, Data2: 0xe52f, Data3: 0x467c, Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xa95664d2, Data2: 0x9614, Data3: 0x4f35, Data4: [8]byte{0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}}
	iidIAudioClient         = windows.GUID{Data1: 0x1cb9ad4c, Data2: 0xdbfa, Data3: 0x4c32, Data4: [8]byte{0xb1, 0x78, 0xc2, 0xf5, 0x68, 0xa7, 0x03, 0xb2}}
	iidIAudioCaptureClient  = windows.GUID{Data1: 0xc8adbd64, Data2: 0xe71e, Data3: 0x48a0, Data4: [8]byte{0xa4, 0xde, 0x18, 0x5c, 0x39, 0x5c, 0xd3, 0x17}}
)

// Vtable slots after IUnknown's three.
const (
	slotEnumGetDefaultAudioEndpoint = 4 // IMMDeviceEnumerator
	slotDeviceActivate              = 3 // IMMDevice
	slotClientInitialize            = 3 // IAudioClient
	slotClientGetMixFormat          = 8
	slotClientStart                 = 10
	slotClientStop                  = 11
	slotClientGetService            = 14
	slotCaptureGetBuffer            = 3 // IAudioCaptureClient
	slotCaptureReleaseBuffer        = 4
	slotCaptureGetNextPacketSize    = 5
)

const (
	audclntStreamflagsLoopback              = 0x00020000
	audclntStreamflagsAutoConvertPCM        = 0x80000000
	audclntStreamflagsSrcDefaultQual        = 0x08000000
	audclntBufferflagsSilent                = 0x2
	audclntEDeviceInvalidated               = 0x88890004
	clsctxAll                               = 0x17
	waveFormatPCM                           = 1
	loopbackBufferHundredNanos       uint64 = 2000000 // 200 ms
)

// waveFormatEx is WAVEFORMATEX (packed to 18 bytes in the Windows headers).
type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
}

// LoopbackFormat is the PCM format Loopback delivers: signed 16-bit
// little-endian, interleaved.
type LoopbackFormat struct {
	SampleRate int
	Channels   int
}

// ErrLoopbackDeviceChanged means the default playback device went away or
// changed; capture must be started again.
var ErrLoopbackDeviceChanged = errors.New("the playback device changed")

// Loopback captures the default playback device until stop is closed,
// calling pcm with consecutive chunks (the slice is reused). ready receives
// the format once capture has started. Silence is delivered as zero samples
// so the stream stays continuous while nothing is playing.
func Loopback(stop <-chan struct{}, ready func(LoopbackFormat), pcm func([]byte)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := Find(procCoInitializeEx, procCoCreateInstance, procCoTaskMemFree); err != nil {
		return err
	}
	if err := hresultError("CoInitializeEx", mustCall(procCoInitializeEx, 0, 0)); err != nil {
		return err
	}
	defer Call(procCoUninitialize) //nolint:errcheck

	var enumerator, device, client, capture unsafe.Pointer
	defer func() {
		release(capture)
		release(client)
		release(device)
		release(enumerator)
	}()
	if err := hresultError("CoCreateInstance(MMDeviceEnumerator)", mustCall(procCoCreateInstance,
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enumerator)))); err != nil {
		return err
	}
	// eRender (0), eConsole (0): the device that plays system sounds.
	if err := hresultError("GetDefaultAudioEndpoint", comCall(enumerator, slotEnumGetDefaultAudioEndpoint, 0, 0, uintptr(unsafe.Pointer(&device)))); err != nil {
		return fmt.Errorf("no playback device: %w", err)
	}
	if err := hresultError("IMMDevice.Activate", comCall(device, slotDeviceActivate,
		uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&client)))); err != nil {
		return err
	}
	var mix *waveFormatEx
	if err := hresultError("GetMixFormat", comCall(client, slotClientGetMixFormat, uintptr(unsafe.Pointer(&mix)))); err != nil {
		return err
	}
	// Ask for 16-bit PCM at the device's rate, at most stereo; the audio
	// engine converts from its float mix format.
	format := waveFormatEx{FormatTag: waveFormatPCM, Channels: min(mix.Channels, 2), SamplesPerSec: mix.SamplesPerSec, BitsPerSample: 16}
	_, _ = Call(procCoTaskMemFree, uintptr(unsafe.Pointer(mix)))
	format.BlockAlign = format.Channels * 2
	format.AvgBytesPerSec = format.SamplesPerSec * uint32(format.BlockAlign)
	const flags = audclntStreamflagsLoopback | audclntStreamflagsAutoConvertPCM | audclntStreamflagsSrcDefaultQual
	// AUDCLNT_SHAREMODE_SHARED (0).
	if err := hresultError("IAudioClient.Initialize", comCall(client, slotClientInitialize, 0, flags,
		uintptr(loopbackBufferHundredNanos), 0, uintptr(unsafe.Pointer(&format)), 0)); err != nil {
		return err
	}
	if err := hresultError("GetService(IAudioCaptureClient)", comCall(client, slotClientGetService,
		uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture)))); err != nil {
		return err
	}
	if err := hresultError("IAudioClient.Start", comCall(client, slotClientStart)); err != nil {
		return err
	}
	defer comCall(client, slotClientStop) //nolint:errcheck
	ready(LoopbackFormat{SampleRate: int(format.SamplesPerSec), Channels: int(format.Channels)})

	// Loopback delivers no packets while nothing is playing, so silence is
	// generated against the clock to keep the stream continuous.
	frameBytes := int(format.BlockAlign)
	bytesPerSecond := int(format.AvgBytesPerSec)
	start := time.Now()
	sent := 0
	var buf []byte
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-tick.C:
		}
		for {
			var frames uint32
			if hr := comCall(capture, slotCaptureGetNextPacketSize, uintptr(unsafe.Pointer(&frames))); int32(hr) < 0 {
				if uint32(hr) == audclntEDeviceInvalidated {
					return ErrLoopbackDeviceChanged
				}
				return hresultError("GetNextPacketSize", hr)
			}
			if frames == 0 {
				break
			}
			var data unsafe.Pointer
			var got, bufFlags uint32
			if hr := comCall(capture, slotCaptureGetBuffer, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&got)),
				uintptr(unsafe.Pointer(&bufFlags)), 0, 0); int32(hr) < 0 {
				if uint32(hr) == audclntEDeviceInvalidated {
					return ErrLoopbackDeviceChanged
				}
				return hresultError("GetBuffer", hr)
			}
			if got == 0 { // AUDCLNT_S_BUFFER_EMPTY
				break
			}
			n := int(got) * frameBytes
			if cap(buf) < n {
				buf = make([]byte, n)
			}
			buf = buf[:n]
			if bufFlags&audclntBufferflagsSilent != 0 || data == nil {
				clear(buf)
			} else {
				copy(buf, unsafe.Slice((*byte)(data), n))
			}
			_ = comCall(capture, slotCaptureReleaseBuffer, uintptr(got))
			pcm(buf)
			sent += n
		}
		// Fill up to 20 ms behind the clock with silence.
		want := int(time.Since(start).Seconds()*float64(bytesPerSecond)) - bytesPerSecond/50
		want -= want % frameBytes
		if gap := want - sent; gap > 0 {
			if cap(buf) < gap {
				buf = make([]byte, gap)
			}
			buf = buf[:gap]
			clear(buf)
			pcm(buf)
			sent += gap
		}
	}
}
