package trace

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"i-tracker/internal/model"
)

func parseFixture(t *testing.T, name string) []rawHop {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var hops []rawHop
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if h, ok := parseHopLine(sc.Text()); ok {
			hops = append(hops, h)
		}
	}
	return hops
}

func rtt(h rawHop) float64 {
	if h.RTTMs == nil {
		return -1
	}
	return *h.RTTMs
}

func TestParseTracertWindows(t *testing.T) {
	// Real `tracert -d` output from German Windows (code page 850), addresses anonymized.
	hops := parseFixture(t, "tracert_windows_de.txt")
	if len(hops) != 10 {
		t.Fatalf("%d hops, want 10", len(hops))
	}
	if hops[0].IP != "192.168.1.1" || rtt(hops[0]) != 1 {
		t.Errorf("hop 1 (<1 ms): %+v rtt=%v", hops[0], rtt(hops[0]))
	}
	if hops[1].Number != 2 || hops[1].IP != "" || hops[1].RTTMs != nil {
		t.Errorf("hop 2 (timeout): %+v", hops[1])
	}
	if hops[4].IP != "203.0.113.216" || rtt(hops[4]) != 2.33 {
		t.Errorf("hop 5: %+v rtt=%v", hops[4], rtt(hops[4]))
	}
	if last := hops[9]; last.Number != 10 || last.IP != "8.8.8.8" {
		t.Errorf("last hop: %+v", last)
	}
}

func TestParseTracerouteLinux(t *testing.T) {
	hops := parseFixture(t, "traceroute_linux.txt")
	if len(hops) != 6 {
		t.Fatalf("%d hops, want 6", len(hops))
	}
	if hops[0].IP != "192.168.1.1" || rtt(hops[0]) != 0.39 {
		t.Errorf("hop 1: %+v rtt=%v", hops[0], rtt(hops[0]))
	}
	if hops[1].IP != "" {
		t.Errorf("hop 2 (* * *): %+v", hops[1])
	}
	// A partial timeout counts as answered.
	if hops[2].IP != "198.51.100.252" || rtt(hops[2]) != 2.21 {
		t.Errorf("hop 3 (partial timeout): %+v rtt=%v", hops[2], rtt(hops[2]))
	}
	// Load balancing: first address wins, RTT over all probes.
	if hops[3].IP != "203.0.113.24" || rtt(hops[3]) != 2.6 {
		t.Errorf("hop 4 (two addresses): %+v rtt=%v", hops[3], rtt(hops[3]))
	}
}

func ptr[T any](v T) *T { return &v }

func hop(n int, ip string, lat, lon *float64, city string) model.TraceHop {
	h := model.TraceHop{HopNumber: n, Responded: ip != "", Latitude: lat, Longitude: lon}
	if ip != "" {
		h.IPAddress = ptr(ip)
	}
	if city != "" {
		h.City = ptr(city)
	}
	return h
}

func TestInterpolate(t *testing.T) {
	hops := []model.TraceHop{
		hop(1, "10.0.0.1", nil, nil, ""),                     // private, no /24 donor: nearest hop
		hop(2, "80.1.1.1", ptr(50.0), ptr(8.0), "Frankfurt"), // donor
		hop(3, "", nil, nil, ""),                             // timeout: stays empty
		hop(4, "90.2.2.2", ptr(52.0), ptr(13.0), "Berlin"),
		hop(5, "80.1.1.9", nil, nil, ""), // same /24 as hop 2: Frankfurt, though hop 4 is closer
	}
	Interpolate(hops, rand.New(rand.NewPCG(1, 2)))

	near := func(h model.TraceHop, lat, lon float64) bool {
		return h.HasLocation() && abs(*h.Latitude-lat) <= jitterDegrees && abs(*h.Longitude-lon) <= jitterDegrees
	}
	if !hops[0].GeoIPInterpolated || !near(hops[0], 50, 8) || *hops[0].City != "? Frankfurt" {
		t.Errorf("hop 1: %+v", hops[0])
	}
	if hops[2].HasLocation() || hops[2].GeoIPInterpolated {
		t.Errorf("timeout hop must not be interpolated: %+v", hops[2])
	}
	if !hops[4].GeoIPInterpolated || !near(hops[4], 50, 8) {
		t.Errorf("hop 5 should copy its /24 neighbor (Frankfurt): lat=%v lon=%v", *hops[4].Latitude, *hops[4].Longitude)
	}
	if hops[1].GeoIPInterpolated || *hops[1].Latitude != 50 {
		t.Errorf("donor must not change: %+v", hops[1])
	}

	none := []model.TraceHop{hop(1, "10.0.0.1", nil, nil, "")}
	Interpolate(none, rand.New(rand.NewPCG(1, 2)))
	if none[0].HasLocation() {
		t.Error("no donor must yield no coordinates")
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// TestHelperProcess stands in for tracert/traceroute: it prints the
// TRACE_HELPER_FIXTURE file line by line with a delay.
func TestHelperProcess(t *testing.T) {
	fixture := os.Getenv("TRACE_HELPER_FIXTURE")
	if fixture == "" {
		return
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		os.Exit(2)
	}
	for _, line := range strings.SplitAfter(string(data), "\n") {
		fmt.Print(line)
		time.Sleep(20 * time.Millisecond)
	}
	os.Exit(0)
}

func newHelperTracer(t *testing.T, fixture string) *Tracer {
	t.Helper()
	abs, _ := filepath.Abs(filepath.Join("testdata", fixture))
	t.Setenv("TRACE_HELPER_FIXTURE", abs)
	tr := NewTracer(nil, os.Args[0], "-test.run=^TestHelperProcess$", "--")
	tr.lookupAddr = func(_ context.Context, ip string) ([]string, error) {
		if ip == "8.8.8.8" {
			return []string{"dns.google."}, nil
		}
		return nil, errors.New("no PTR")
	}
	return tr
}

func TestTracerRun(t *testing.T) {
	tr := newHelperTracer(t, "traceroute_linux.txt")
	var live []int
	res := tr.Run(context.Background(), "8.8.8.8", DefaultOptions(), func(h model.TraceHop) { live = append(live, h.HopNumber) })

	if !res.Completed || res.ErrorMessage != nil {
		t.Fatalf("not completed: %v", *res.ErrorMessage)
	}
	// The trace stops at the target (hop 5); the line after it is not read.
	if res.TotalHops != 5 || len(res.Hops) != 5 || len(live) != 5 {
		t.Fatalf("hops: total=%d, stored=%d, live=%d", res.TotalHops, len(res.Hops), len(live))
	}
	last := res.Hops[4]
	if last.Hostname == nil || *last.Hostname != "dns.google" || !last.Responded {
		t.Errorf("target hop: %+v", last)
	}
	if res.Hops[1].Responded || res.Hops[1].IPAddress != nil {
		t.Errorf("timeout hop: %+v", res.Hops[1])
	}
	if res.TotalRTTMs == nil || *res.TotalRTTMs != 4.54 {
		t.Errorf("total_rtt_ms = %v, want target RTT (4.54)", res.TotalRTTMs)
	}
	if res.DestinationIP == nil || *res.DestinationIP != "8.8.8.8" || res.StartedAt == nil || res.CompletedAt == nil {
		t.Errorf("metadata: %+v", res)
	}
}

func TestTracerMissingCommand(t *testing.T) {
	tr := NewTracer(nil, filepath.Join(t.TempDir(), "no-traceroute"))
	res := tr.Run(context.Background(), "127.0.0.1", DefaultOptions(), nil)
	if res.Completed || res.ErrorMessage == nil || !strings.Contains(*res.ErrorMessage, "not found") {
		t.Errorf("want error: %+v", res)
	}
}

func TestGeoIPLookup(t *testing.T) {
	var none *GeoIP
	if none.Lookup("8.8.8.8") != nil {
		t.Error("nil GeoIP must return nil")
	}
	if NewGeoIP().Lookup("8.8.8.8") != nil || NewGeoIP().Info().Source != "" {
		t.Error("empty GeoIP must return nil and no source")
	}

	// Uses a local database (not in the repo) if present.
	dir := filepath.Join("..", "..", "geoip")
	geo, _, err := OpenGeoIP(filepath.Join(dir, "city.mmdb"), filepath.Join(dir, "GeoLite2-City.mmdb"))
	if err != nil {
		t.Skip("no GeoIP database in geoip/")
	}
	defer geo.Close()
	if info := geo.Info(); info.Source == "" || info.BuildDate == "" {
		t.Errorf("Info: %+v", info)
	}

	if loc := geo.Lookup("8.8.8.8"); loc == nil || loc.CountryCode != "US" {
		t.Errorf("8.8.8.8: %+v", loc)
	}
	if loc := geo.Lookup("192.168.1.1"); loc != nil {
		t.Errorf("private address must have no location: %+v", loc)
	}
}
