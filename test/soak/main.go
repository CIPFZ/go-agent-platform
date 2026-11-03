// Command soak drives a running vm-agent with a continuous capture and input
// loop and samples its resource usage, for the 24 hour stability check of
// spec 13.1 acceptance 3 and section 12: with the sample one hour after
// start as baseline, RSS, goroutines and handles must not grow by more than
// 10 % after 24 hours.
//
//	soak -url http://127.0.0.1:7070 -pid 1234 -duration 24h -out soak.csv
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:7070", "agent URL")
	pid := flag.Int("pid", 0, "agent pid to sample")
	duration := flag.Duration("duration", 24*time.Hour, "run time")
	interval := flag.Duration("sample", time.Minute, "sampling interval")
	pace := flag.Duration("pace", 200*time.Millisecond, "pause between actions")
	out := flag.String("out", "soak.csv", "CSV output")
	region := flag.String("region", "100,100,600,400", "x,y,w,h area for pointer actions")
	report := flag.String("report", "", "evaluate an existing CSV and exit")
	flag.Parse()
	if *report != "" {
		text, ok, err := Report(*report)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(text)
		if !ok {
			os.Exit(1)
		}
		return
	}
	if *pid == 0 {
		log.Fatal("-pid is required")
	}
	var rx, ry, rw, rh int
	if _, err := fmt.Sscanf(*region, "%d,%d,%d,%d", &rx, &ry, &rw, &rh); err != nil {
		log.Fatal(err)
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	w := csv.NewWriter(f)
	if info, _ := f.Stat(); info.Size() == 0 {
		w.Write([]string{"time", "elapsed_s", "rss_kib", "handles", "threads", "goroutines", "actions", "errors", "last_error"})
		w.Flush()
	}
	c := client.New(*url)
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	var actions, errors atomic.Int64
	var lastErr atomic.Value
	lastErr.Store("")
	go func() {
		i := 0
		for ctx.Err() == nil {
			var err error
			x, y := rx+(i*37)%rw, ry+(i*53)%rh
			switch i % 5 {
			case 0:
				_, err = c.ScreenCapture(ctx, &client.CaptureRequest{Cursor: client.Ptr(true)})
			case 1:
				err = c.MouseMove(ctx, &client.MouseMoveRequest{X: x, Y: y, DurationMS: client.Ptr(40)})
			case 2:
				err = c.MouseClick(ctx, &client.MouseClickRequest{X: client.Ptr(x), Y: client.Ptr(y)})
			case 3:
				err = c.KeyType(ctx, &client.KeyTypeRequest{Text: "soak é世 " + strconv.Itoa(i)})
			case 4:
				_, err = c.InputBatch(ctx, &client.BatchRequest{Actions: []client.BatchAction{
					{Type: "key.press", Key: client.Ptr("backspace")}, {Type: "wait", MS: client.Ptr(5)}, {Type: "mouse.scroll", DY: client.Ptr(1)},
				}})
			}
			if err != nil && ctx.Err() == nil {
				errors.Add(1)
				lastErr.Store(err.Error())
			}
			actions.Add(1)
			i++
			time.Sleep(*pace)
		}
	}()
	start := time.Now()
	sample := func() {
		rss, handles, threads := procStats(*pid)
		g := goroutines(*url)
		w.Write([]string{time.Now().UTC().Format(time.RFC3339), strconv.Itoa(int(time.Since(start).Seconds())),
			strconv.FormatInt(rss, 10), strconv.Itoa(handles), strconv.Itoa(threads), strconv.Itoa(g),
			strconv.FormatInt(actions.Load(), 10), strconv.FormatInt(errors.Load(), 10), lastErr.Load().(string)})
		w.Flush()
	}
	sample()
	t := time.NewTicker(*interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			sample()
			return
		case <-t.C:
			sample()
		}
	}
}

// goroutines reads vm_agent_goroutines from /v1/metrics, or -1.
func goroutines(url string) int {
	resp, err := http.Get(url + "/v1/metrics")
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	s := bufio.NewScanner(resp.Body)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "vm_agent_goroutines "); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return -1
}
