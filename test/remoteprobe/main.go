// Command remoteprobe connects to a vm-agent's web remote desktop the way
// the page does and reports what arrives: frame rate, bitrate, keyframes and
// the capture pipeline. It is used to verify a deployment without a browser.
//
//	go run ./test/remoteprobe -agent http://HOST:7070 -seconds 10
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

type envelope struct {
	OK     bool `json:"ok"`
	Result struct {
		SessionID   string `json:"session_id"`
		SDP         string `json:"sdp"`
		FPS         int    `json:"fps"`
		BitrateKbps int    `json:"bitrate_kbps"`
		Display     struct{ X, Y, W, H int }
	} `json:"result"`
	Error json.RawMessage `json:"error"`
}

func main() {
	agent := flag.String("agent", "http://127.0.0.1:7070", "vm-agent base URL")
	seconds := flag.Int("seconds", 10, "how long to receive video")
	fps := flag.Int("fps", 30, "requested frame rate")
	kbps := flag.Int("bitrate", 0, "requested bitrate in kbit/s, 0 for the default")
	holder := flag.String("holder", "remoteprobe", "name shown while the desktop is taken")
	token := flag.String("token", "", "access token, for an agent started with -auth-token-file")
	wiggle := flag.Bool("wiggle", false, "move the pointer in a small square and back, to test input")
	flag.Parse()
	if err := run(*agent, *seconds, *fps, *kbps, *holder, *token, *wiggle); err != nil {
		fmt.Fprintln(os.Stderr, "remoteprobe:", err)
		os.Exit(1)
	}
}

func run(agent string, seconds, fps, kbps int, holder, token string, wiggle bool) error {
	var se webrtc.SettingEngine
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		return err
	}

	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		return err
	}
	var audioPackets, audioBytes, audioGaps atomic.Int64
	var packets, bytesIn, frames, keyframes, gaps atomic.Int64
	var firstPacket, firstKey atomic.Int64
	var codec atomic.Value
	start := time.Now()
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Kind() == webrtc.RTPCodecTypeAudio {
			var last uint16
			have := false
			for {
				pkt, _, err := tr.ReadRTP()
				if err != nil {
					return
				}
				if have && pkt.SequenceNumber != last+1 {
					audioGaps.Add(1)
				}
				last, have = pkt.SequenceNumber, true
				audioPackets.Add(1)
				audioBytes.Add(int64(len(pkt.Payload)))
			}
		}
		codec.Store(tr.Codec().MimeType + " " + tr.Codec().SDPFmtpLine)
		var last uint16
		have := false
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			firstPacket.CompareAndSwap(0, int64(time.Since(start)))
			if have && pkt.SequenceNumber != last+1 {
				gaps.Add(1)
			}
			last, have = pkt.SequenceNumber, true
			packets.Add(1)
			bytesIn.Add(int64(len(pkt.Payload)))
			if pkt.Marker {
				frames.Add(1)
			}
			if isIDR(pkt.Payload) {
				keyframes.Add(1)
				firstKey.CompareAndSwap(0, int64(time.Since(start)))
			}
		}
	})

	input, err := pc.CreateDataChannel("input", nil)
	if err != nil {
		return err
	}
	unordered, none := false, uint16(0)
	if _, err := pc.CreateDataChannel("pointer", &webrtc.DataChannelInit{Ordered: &unordered, MaxRetransmits: &none}); err != nil {
		return err
	}
	control, err := pc.CreateDataChannel("control", nil)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	seen := map[string]string{}
	control.OnMessage(func(m webrtc.DataChannelMessage) {
		var msg map[string]any
		if json.Unmarshal(m.Data, &msg) != nil {
			return
		}
		t, _ := msg["t"].(string)
		delete(msg, "png")
		data, _ := json.Marshal(msg)
		mu.Lock()
		seen[t] = string(data)
		mu.Unlock()
	})
	inputOpen := make(chan struct{})
	input.OnOpen(func() { close(inputOpen) })

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	<-gathered

	req := map[string]any{"sdp": pc.LocalDescription().SDP, "holder": holder, "client_id": "remoteprobe", "fps": fps}
	if kbps > 0 {
		req["bitrate_kbps"] = kbps
	}
	body, _ := json.Marshal(req)
	post, err := http.NewRequest(http.MethodPost, agent+"/v1/remote/sessions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	post.Header.Set("Content-Type", "application/json")
	if token != "" {
		post.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(post)
	if err != nil {
		return err
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return fmt.Errorf("open session: HTTP %d %s", resp.StatusCode, raw)
	}
	defer func() {
		r, _ := http.NewRequest(http.MethodDelete, agent+"/v1/remote/sessions/"+env.Result.SessionID, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if resp, err := http.DefaultClient.Do(r); err == nil {
			resp.Body.Close()
		}
	}()
	fmt.Printf("session %s  display %dx%d  %d fps  %d kbit/s\n", env.Result.SessionID, env.Result.Display.W, env.Result.Display.H, env.Result.FPS, env.Result.BitrateKbps)
	start = time.Now()
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: env.Result.SDP}); err != nil {
		return err
	}

	if wiggle {
		select {
		case <-inputOpen:
			d := env.Result.Display
			cx, cy := d.X+d.W/2, d.Y+d.H/2
			for _, p := range [][2]int{{0, 0}, {60, 0}, {60, 60}, {0, 60}, {0, 0}} {
				_ = input.SendText(fmt.Sprintf(`{"t":"mm","x":%d,"y":%d}`, cx+p[0], cy+p[1]))
				time.Sleep(150 * time.Millisecond)
			}
		case <-time.After(15 * time.Second):
			return fmt.Errorf("the input channel did not open")
		}
	}

	var prevFrames, prevBytes, prevAudio int64
	for i := 1; i <= seconds; i++ {
		time.Sleep(time.Second)
		f, b, a := frames.Load(), bytesIn.Load(), audioBytes.Load()
		fmt.Printf("  %2ds  %3d frames  %6d kbit/s  audio %3d kbit/s\n", i, f-prevFrames, (b-prevBytes)*8/1000, (a-prevAudio)*8/1000)
		prevFrames, prevBytes, prevAudio = f, b, a
	}
	elapsed := time.Since(start).Seconds()
	c, _ := codec.Load().(string)
	fmt.Printf("codec        %s\n", c)
	fmt.Printf("first packet %d ms, first keyframe %d ms after the answer\n", firstPacket.Load()/1e6, firstKey.Load()/1e6)
	fmt.Printf("total        %d packets, %d frames (%.1f fps), %d keyframes, %d sequence gaps, %.0f kbit/s\n",
		packets.Load(), frames.Load(), float64(frames.Load())/elapsed, keyframes.Load(), gaps.Load(), float64(bytesIn.Load())*8/1000/elapsed)
	fmt.Printf("audio        %d packets (%.0f per second), %d sequence gaps, %.0f kbit/s\n", audioPackets.Load(), float64(audioPackets.Load())/elapsed,
		audioGaps.Load(), float64(audioBytes.Load())*8/1000/elapsed)
	mu.Lock()
	for _, t := range []string{"hello", "status", "cursor", "error", "bye"} {
		if v, ok := seen[t]; ok {
			fmt.Printf("control      %s\n", v)
		}
	}
	mu.Unlock()
	if frames.Load() == 0 {
		return fmt.Errorf("no video arrived")
	}
	return nil
}

// isIDR reports whether an H.264 RTP payload carries (the start of) an IDR
// slice: a single NAL unit, an aggregation packet or the first fragment.
func isIDR(p []byte) bool {
	if len(p) < 2 {
		return false
	}
	switch p[0] & 0x1f {
	case 5:
		return true
	case 24: // STAP-A
		for i := 1; i+2 < len(p); {
			n := int(p[i])<<8 | int(p[i+1])
			if p[i+2]&0x1f == 5 {
				return true
			}
			i += 2 + n
		}
	case 28: // FU-A
		return p[1]&0x80 != 0 && p[1]&0x1f == 5
	}
	return false
}
