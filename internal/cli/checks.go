package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/edge"
)

// boxChecks are the box-level health checks behind /v1/status. They read
// the machine directly, so they keep working when app services are down.
func boxChecks(home string, ed *edge.Edge, started time.Time) []api.Check {
	var out []api.Check
	if free, total, err := diskSpace(home); err == nil && total > 0 {
		pct := 100 * float64(total-free) / float64(total)
		out = append(out, api.Check{Name: "disk", OK: pct < 90,
			Detail: fmt.Sprintf("%.0f%% used, %s free of %s", pct, bytesHuman(free), bytesHuman(total))})
	}
	if avail, total, ok := memInfo(); ok {
		out = append(out, api.Check{Name: "memory", OK: avail > total/20,
			Detail: fmt.Sprintf("%s available of %s", bytesHuman(avail), bytesHuman(total))})
	}
	if ed != nil {
		_, err := ed.RootCAPEM()
		out = append(out, api.Check{Name: "edge", OK: err == nil, Detail: errOr(err, "HTTPS edge serving with the box's CA")})
	}
	return out
}

func errOr(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

// memInfo reads /proc/meminfo (Linux only; elsewhere it reports !ok).
func memInfo() (avail, total uint64, ok bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return avail, total, total > 0
}

func bytesHuman(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
