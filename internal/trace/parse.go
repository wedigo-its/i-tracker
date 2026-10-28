// Package trace runs route traces (tracert on Windows, traceroute elsewhere),
// adds GeoIP locations and interpolates missing coordinates.
package trace

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	hopLineRe = regexp.MustCompile(`^\s*(\d+)\s+(.+)$`)
	ipv4Re    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	rttRe     = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*ms\b`)
)

// rawHop is a parsed hop line before enrichment.
type rawHop struct {
	Number int
	IP     string   // empty if the hop did not answer
	RTTMs  *float64 // mean of all measured RTTs
}

// parseHopLine parses a tracert/traceroute line (run with -d/-n); ok is false
// for header and footer lines. Any IP marks the hop as answered, regardless of
// the timeout text's language or partial timeouts; with load balancing the
// first IP wins. "<1 ms" counts as 1 ms.
func parseHopLine(line string) (h rawHop, ok bool) {
	m := hopLineRe.FindStringSubmatch(line)
	if m == nil {
		return h, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return h, false
	}
	h.Number = n
	rest := m[2]

	h.IP = ipv4Re.FindString(rest)
	if h.IP == "" {
		return h, true
	}

	var sum float64
	rtts := rttRe.FindAllStringSubmatch(rest, -1)
	for _, r := range rtts {
		v, _ := strconv.ParseFloat(r[1], 64)
		sum += v
	}
	if len(rtts) > 0 {
		avg := round2(sum / float64(len(rtts)))
		h.RTTMs = &avg
	}
	return h, true
}

// subnet24 returns the first three octets of an IPv4 address ("1.2.3") or "".
func subnet24(ip string) string {
	i := strings.LastIndexByte(ip, '.')
	if i < 0 || strings.Count(ip, ".") != 3 {
		return ""
	}
	return ip[:i]
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
