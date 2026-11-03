package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
)

// Report evaluates a soak CSV: the first sample at or after one hour is the
// baseline, the last sample is compared with it.
func Report(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) < 2 {
		return "", false, fmt.Errorf("no samples in %s: %v", path, err)
	}
	var base, last []string
	for _, r := range rows[1:] {
		if s, _ := strconv.Atoi(r[1]); s >= 3600 && base == nil {
			base = r
		}
		last = r
	}
	if base == nil {
		return "", false, fmt.Errorf("run shorter than one hour")
	}
	ok := true
	out := ""
	for i, name := range []string{"rss_kib", "handles", "threads", "goroutines"} {
		b, _ := strconv.ParseFloat(base[2+i], 64)
		l, _ := strconv.ParseFloat(last[2+i], 64)
		if b <= 0 {
			out += fmt.Sprintf("%s: not sampled\n", name)
			continue
		}
		growth := (l - b) / b * 100
		verdict := "ok"
		if growth > 10 && name != "threads" {
			verdict, ok = "EXCEEDS 10%", false
		}
		out += fmt.Sprintf("%s: baseline %.0f (at %ss) -> %.0f (at %ss): %+.1f%% %s\n", name, b, base[1], l, last[1], growth, verdict)
	}
	out += fmt.Sprintf("actions %s, errors %s\n", last[6], last[7])
	return out, ok, nil
}
