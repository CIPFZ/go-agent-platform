package api

import (
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
	// Bound enumeration well inside the request timeout so a hung display
	// still lets probe answer.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if displays, err := a.screen.Displays(ctx); err == nil && len(displays) > 0 {
		ds.Count = len(displays)
		p := displays[0]
		ds.Primary = &protocol.PrimaryDisplay{W: p.Bounds.W, H: p.Bounds.H, Scale: p.Scale}
	}
	return ds
}

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

// actions lists the supported action groups. Display-bound groups are
// omitted while the display is not ready (spec section 9).
func (a *API) actions(displayReady bool) []string {
	out := []string{"exec", "process"}
	if displayReady {
		out = append(out, "screen", "input", "window")
	}
	return out
}

const (
	fileReadMax     int64 = 64 << 20
	downloadMax     int64 = 1 << 30
	batchMaxActions       = 200
)
