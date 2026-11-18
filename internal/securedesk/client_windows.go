//go:build windows

package securedesk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/CIPFZ/go-agent-platform/internal/actions/remote"
)

// NewClient returns the client of the helper in this process's session.
func NewClient() remote.SecureDesktop {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return nil
	}
	return &client{path: PipePath(session)}
}

type client struct {
	path string

	mu      sync.Mutex
	input   net.Conn
	checked time.Time
	ok      bool
}

const dialTimeout = 300 * time.Millisecond

func (c *client) dial(op request) (net.Conn, error) {
	t := dialTimeout
	conn, err := winio.DialPipe(c.path, &t)
	if err != nil {
		return nil, err
	}
	line, _ := json.Marshal(op)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// Available keeps one connection open for input; whether it could be made
// is remembered for a second so callers may ask often.
func (c *client) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.input != nil {
		return true
	}
	if time.Since(c.checked) < time.Second {
		return c.ok
	}
	c.checked = time.Now()
	conn, err := c.dial(request{Op: "input"})
	c.ok = err == nil
	if err == nil {
		c.input = conn
	}
	return c.ok
}

func (c *client) Input(data []byte) error {
	if !c.Available() {
		return errors.New("the secure desktop helper is not running")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.input == nil {
		return errors.New("the secure desktop helper went away")
	}
	_ = c.input.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.input.Write(append(append([]byte(nil), data...), '\n')); err != nil {
		c.input.Close()
		c.input, c.checked = nil, time.Time{}
		return err
	}
	return nil
}

func (c *client) Video(ctx context.Context, v remote.SecureVideo) error {
	conn, err := c.dial(request{Op: "video", FPS: v.FPS, Kbps: v.BitrateKbps, Port: v.Port, W: v.W, H: v.H})
	if err != nil {
		return err
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() {
		// The helper writes only when the capture ended by itself.
		line, rerr := bufio.NewReader(conn).ReadBytes('\n')
		var reply request
		if json.Unmarshal(line, &reply) == nil && reply.Error != "" {
			done <- errors.New(reply.Error)
			return
		}
		if rerr == nil {
			rerr = errors.New("the helper ended the capture")
		}
		done <- rerr
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		return err
	}
}
