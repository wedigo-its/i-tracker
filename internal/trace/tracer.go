package trace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"i-tracker/internal/db"
	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
)

// maxDuration caps a whole trace; hops found so far are kept.
const maxDuration = 90 * time.Second

// reverseDNSTimeout caps the reverse lookup per hop.
const reverseDNSTimeout = 1500 * time.Millisecond

// Options control a trace.
type Options struct {
	MaxHops int
	WaitSec int // wait per probe
	Probes  int // probes per hop (traceroute only; tracert always sends 3)
}

// DefaultOptions returns 30 hops, 2 s wait per probe, 3 probes.
func DefaultOptions() Options { return Options{MaxHops: 30, WaitSec: 2, Probes: 3} }

// HopFunc is called for each hop as soon as it is known (live view).
type HopFunc func(model.TraceHop)

// Tracer runs route traces.
type Tracer struct {
	geo *GeoIP
	// command replaces tracert/traceroute (tests); the target IP is appended.
	command    []string
	lookupAddr func(ctx context.Context, ip string) ([]string, error)
}

// NewTracer creates a Tracer; geo may be nil (no locations). command optionally
// replaces tracert/traceroute; the target IP is appended.
func NewTracer(geo *GeoIP, command ...string) *Tracer {
	t := &Tracer{geo: geo, lookupAddr: net.DefaultResolver.LookupAddr}
	if len(command) > 0 {
		t.command = command
	}
	return t
}

// Run traces the route to destination, stopping once the target answers.
// Errors are recorded in the trace (Completed = false).
func (t *Tracer) Run(ctx context.Context, destination string, opts Options, onHop HopFunc) *model.Trace {
	started := db.Now()
	tr := &model.Trace{DestinationHost: destination, StartedAt: &started, Hops: []model.TraceHop{}}
	defer func() {
		done := db.Now()
		tr.CompletedAt = &done
		tr.TotalHops = len(tr.Hops)
	}()

	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	destIP, err := resolveIPv4(ctx, destination)
	if err != nil {
		tr.SetError(i18n.Wrap(err, "err.trace.resolve"))
		return tr
	}
	tr.DestinationIP = &destIP
	if src := sourceIP(destIP); src != "" {
		tr.SourceIP = &src
	}

	err = t.trace(ctx, destIP, opts, func(raw rawHop) bool {
		hop := t.enrich(ctx, raw)
		tr.Hops = append(tr.Hops, hop)
		if onHop != nil {
			onHop(hop)
		}
		return raw.IP == destIP
	})

	Interpolate(tr.Hops, rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
	// Total RTT = RTT of the last answering hop.
	for i := len(tr.Hops) - 1; i >= 0; i-- {
		if tr.Hops[i].RTTMs != nil {
			tr.TotalRTTMs = tr.Hops[i].RTTMs
			break
		}
	}
	if err != nil {
		tr.SetError(err)
	} else {
		tr.Completed = true
	}
	return tr
}

// trace runs the tool and passes each hop line to handle; handle returning
// true (target reached) kills the tool.
func (t *Tracer) trace(ctx context.Context, destIP string, o Options, handle func(rawHop) bool) error {
	name, args := t.commandLine(destIP, o)
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return i18n.Wrap(err, "err.trace.tool_start", name)
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return i18n.E("err.trace.tool_missing", name)
		}
		return i18n.Wrap(err, "err.trace.tool_start", name)
	}

	reached := false
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		h, ok := parseHopLine(sc.Text())
		if !ok {
			continue
		}
		if handle(h) {
			reached = true
			cmd.Process.Kill()
			break
		}
	}
	waitErr := cmd.Wait()

	switch {
	case reached:
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return i18n.E("err.timeout", maxDuration)
	case ctx.Err() != nil:
		return i18n.E("err.trace.aborted")
	case waitErr != nil:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return i18n.Foreign(msg)
		}
		return i18n.Wrap(waitErr, "err.trace.tool_exit", name)
	}
	return nil
}

func (t *Tracer) commandLine(destIP string, o Options) (string, []string) {
	if t.command != nil {
		return t.command[0], append(append([]string{}, t.command[1:]...), destIP)
	}
	if runtime.GOOS == "windows" {
		return "tracert", []string{"-d", "-h", strconv.Itoa(o.MaxHops), "-w", strconv.Itoa(o.WaitSec * 1000), destIP}
	}
	return "traceroute", []string{"-n", "-m", strconv.Itoa(o.MaxHops), "-w", strconv.Itoa(o.WaitSec), "-q", strconv.Itoa(o.Probes), destIP}
}

// enrich adds hostname and location to an answered hop.
func (t *Tracer) enrich(ctx context.Context, raw rawHop) model.TraceHop {
	hop := model.TraceHop{HopNumber: raw.Number, Responded: raw.IP != ""}
	if !hop.Responded {
		return hop
	}
	ip := raw.IP
	hop.IPAddress = &ip
	hop.RTTMs = raw.RTTMs

	dnsCtx, cancel := context.WithTimeout(ctx, reverseDNSTimeout)
	if names, err := t.lookupAddr(dnsCtx, ip); err == nil && len(names) > 0 {
		name := strings.TrimSuffix(names[0], ".")
		hop.Hostname = &name
	}
	cancel()

	if loc := t.geo.Lookup(ip); loc != nil {
		lat, lon := loc.Latitude, loc.Longitude
		hop.Latitude, hop.Longitude = &lat, &lon
		hop.City = nonEmpty(loc.City)
		hop.Country = nonEmpty(loc.Country)
		hop.CountryCode = nonEmpty(loc.CountryCode)
	}
	return hop
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resolveIPv4 returns host's IPv4 address. Tracing the address keeps the tool's
// target identical to the one used for the "target reached" check.
func resolveIPv4(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
		return "", i18n.E("err.trace.ipv4_only")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return "", err
	}
	return ips[0].String(), nil
}

// sourceIP returns the local address used to reach destIP; a UDP "connect"
// sends no packets.
func sourceIP(destIP string) string {
	conn, err := net.Dial("udp4", net.JoinHostPort(destIP, "80"))
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
