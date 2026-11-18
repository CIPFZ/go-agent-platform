//go:build windows

// Command vdisplay drives the virtual display driver the way vm-agent does,
// to check a deployment: status, plug [WIDTH HEIGHT SCALE], unplug.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
)

func main() {
	win.InitDPIAwareness()
	cmd := "status"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "status":
	case "plug":
		var name string
		if name, err = win.PlugVirtualDisplay(5 * time.Second); err == nil {
			fmt.Println("plugged", name)
			if len(os.Args) >= 4 {
				w, _ := strconv.Atoi(os.Args[2])
				h, _ := strconv.Atoi(os.Args[3])
				fmt.Println("mode:", win.SetDisplayMode(name, w, h))
			}
			if len(os.Args) >= 5 {
				s, _ := strconv.ParseFloat(os.Args[4], 64)
				fmt.Println("scale:", win.SetDisplayScale(name, s))
			}
		}
	case "scale":
		s, _ := strconv.ParseFloat(os.Args[2], 64)
		for _, name := range win.VirtualDisplays() {
			fmt.Println("scale", name, win.SetDisplayScale(name, s))
		}
	case "unplug":
		err = win.UnplugVirtualDisplay()
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Println("error:", err)
	}
	time.Sleep(time.Second)
	fmt.Println("installed:", win.VirtualDisplayInstalled(), "| virtual monitors:", win.VirtualDisplays(), "| compositor active:", win.DisplayActive())
	if mons, merr := win.Monitors(); merr == nil {
		for _, m := range mons {
			fmt.Printf("monitor %dx%d at %d,%d scale %.2f primary %v\n", m.Rect.Right-m.Rect.Left, m.Rect.Bottom-m.Rect.Top, m.Rect.Left, m.Rect.Top, m.Scale, m.Primary)
		}
	}
	if err != nil {
		os.Exit(1)
	}
}
