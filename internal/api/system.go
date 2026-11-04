package api

import (
	"bytes"
	"context"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/exec"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
	"github.com/CIPFZ/go-agent-platform/internal/version"
)

// Default timeouts of spec section 7.
const (
	timeoutSystem = 5 * time.Second
	timeoutScreen = 5 * time.Second
)

// displayStatus collects the display block shared by probe and capabilities.
func (a *API) displayStatus(ctx context.Context) protocol.DisplayStatus {
	st := a.session()
	ds := protocol.DisplayStatus{Ready: st.DisplayReady && !st.Locked, Degraded: a.screen.Degraded(), DPIAwareness: a.dpi}
	if !st.DisplayReady {
		return ds
	}
	// Enumeration may wait for the first Wayland screenshot; give it the
	// request's time but keep a margin so probe still answers when the
	// display hangs.
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-enumerationMargin))
		defer cancel()
	}
	displays, err := a.screen.Displays(ctx)
	if err != nil || len(displays) == 0 {
		// A display that cannot be enumerated cannot be captured either:
		// report it as not ready rather than ready with zero displays.
		ds.Ready = false
		return ds
	}
	ds.Count = len(displays)
	p := displays[0]
	ds.Primary = &protocol.PrimaryDisplay{W: p.Bounds.W, H: p.Bounds.H, Scale: p.Scale}
	return ds
}

// enumerationMargin is kept free of the request deadline when probe and
// capabilities enumerate displays.
const enumerationMargin = 250 * time.Millisecond

func (a *API) probe(c *server.Call) (any, error) {
	ctx := c.Context(timeoutSystem)
	st := a.session()
	now := a.now()
	return protocol.ProbeResult{
		Version:       version.Version,
		APIVersion:    protocol.APIVersion,
		Platform:      platformName(),
		StartedAt:     a.started.UTC(),
		UptimeSeconds: int64(now.Sub(a.started).Seconds()),
		LastInputAt:   a.lastInput.Load(),
		Display:       a.displayStatus(ctx),
		SessionLocked: st.Locked,
		SessionType:   st.Type,
		UACPrompt:     st.UACPrompt,
	}, nil
}

func (a *API) capabilities(c *server.Call) (any, error) {
	ctx := c.Context(timeoutSystem)
	st := a.session()
	ds := a.displayStatus(ctx)
	return protocol.CapabilitiesResult{
		Platform: platformName(),
		Version:  version.Version,
		Display:  ds,
		Session:  protocol.SessionStatus{Locked: st.Locked, Type: st.Type, UACPrompt: st.UACPrompt},
		Actions:  a.actions(ds.Ready),
		Limits: protocol.Limits{
			JSONBodyMaxBytes:     server.JSONBodyMax,
			MultipartMaxBytes:    server.MultipartBodyMax,
			ExecOutputMaxBytes:   exec.MaxOutputLimit,
			FileReadMaxBytes:     fileReadMax,
			DownloadMaxBytes:     downloadMax,
			BatchMaxActions:      batchMaxActions,
			MaxConcurrency:       a.cfg.MaxConcurrency,
			SyncTimeoutMaxMS:     server.SyncTimeoutMax.Milliseconds(),
			TransferTimeoutMaxMS: server.TransferTimeout.Milliseconds(),
		},
	}, nil
}

// actions lists the supported action groups in the order of spec section
// 9. Display-bound groups are omitted while the display is not ready.
func (a *API) actions(displayReady bool) []string {
	var out []string
	for _, g := range []struct {
		name    string
		display bool
		wayland bool // available in a Wayland session
	}{{"screen", true, true}, {"input", true, true}, {"window", true, false}, {"exec", false, true}, {"process", false, true},
		{"file", false, true}, {"clipboard", true, false}, {"record", true, false}} {
		if (displayReady || !g.display) && (g.wayland || !a.wayland) && (g.name != "record" || a.recorder.Available()) {
			out = append(out, g.name)
		}
	}
	if displayReady && !a.wayland && a.a11yAvailable() {
		out = append(out, "a11y")
	}
	// The optional uploader is listed on its own: uploading finished
	// recordings needs neither the display nor ffmpeg.
	if a.uploader != nil {
		out = append(out, "record_upload")
	}
	return out
}

const (
	fileReadMax     int64 = 64 << 20
	downloadMax     int64 = 1 << 30
	batchMaxActions       = 200
)

func (a *API) metrics(c *server.Call) (any, error) {
	_ = c.Context(timeoutSystem)
	a.statsMu.Lock()
	stats := a.stats
	a.statsMu.Unlock()
	if stats == nil {
		return nil, protocol.Errorf(protocol.CodeInternal, "metrics are not connected")
	}
	var buf bytes.Buffer
	stats.WritePrometheus(&buf)
	return &server.Stream{ContentType: "text/plain; version=0.0.4; charset=utf-8", Size: int64(buf.Len()), Body: &buf}, nil
}
