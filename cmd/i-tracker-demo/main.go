// Command i-tracker-demo erzeugt eine erfundene Messumgebung mit 30 Tagen
// Verlauf und startet darauf die echte Oberfläche – ohne echte Messungen.
// Gedacht für Screenshots und Vorführungen. Alle Hosts liegen in
// Dokumentationsbereichen (example.net, 192.0.2.0/24, 198.51.100.0/24,
// 203.0.113.0/24, ASN 64496–64511).
//
//	go run ./cmd/i-tracker-demo -db demo.db -listen 127.0.0.1:8098
//
// Anmeldung: admin / demo-password (ohne Anmeldung zeigt die Oberfläche die
// Gastansicht).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"time"

	"i-tracker/internal/auth"
	"i-tracker/internal/db"
	"i-tracker/internal/i18n"
	"i-tracker/internal/iperf"
	"i-tracker/internal/model"
	"i-tracker/internal/scheduler"
	"i-tracker/internal/store"
	"i-tracker/internal/trace"
	"i-tracker/internal/web"
)

const (
	adminUser     = "admin"
	adminPassword = "demo-password"
)

func main() {
	dbPath := flag.String("db", "i-tracker-demo.db", "demo database (created anew)")
	listen := flag.String("listen", "127.0.0.1:8098", "address of the web interface")
	days := flag.Int("days", 30, "days of history")
	seedOnly := flag.Bool("seed-only", false, "only create the database, do not start the web interface")
	flag.Parse()

	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(*dbPath + suffix)
	}
	conn, err := db.Open(*dbPath)
	if err != nil {
		fail(err)
	}
	defer conn.Close()
	st := store.New(conn)
	ctx := context.Background()

	admin, err := auth.NewAdmin(adminUser, "admin@example.net", adminPassword, false)
	if err != nil {
		fail(err)
	}
	if err := st.CreateUser(ctx, admin); err != nil {
		fail(err)
	}

	end := time.Now().Add(-4 * time.Minute)
	start := end.Add(-time.Duration(*days) * 24 * time.Hour)
	d := &demo{st: st, conn: conn, rnd: rand.New(rand.NewPCG(2026, 12))}
	n := 0
	for _, p := range profiles() {
		c, err := d.seedServer(ctx, p, start, end)
		if err != nil {
			fail(err)
		}
		n += c
	}
	fmt.Printf("Demo database %s: %d servers, %d tests\n", *dbPath, len(profiles()), n)
	if *seedOnly {
		return
	}

	geo := trace.NewGeoIP()
	tracer := trace.NewTracer(geo)
	runner := iperf.NewRunner(st, iperf.FindBinary(""))
	defer runner.Stop()
	runner.Prepare("", false) // meldet iperf3 als verfügbar, wenn installiert
	srv := web.NewServer(web.Deps{
		Store:      st,
		Tokens:     auth.NewTokens("demo-secret-not-for-production-0123456789", 12*time.Hour),
		Runner:     runner,
		Tracer:     tracer,
		Scheduler:  scheduler.New(st, runner, tracer), // nicht gestartet: keine echten Messungen
		GeoIP:      geo.Info,
		PublicRead: true,
	})
	slog.Info("Demo web interface running, stop with Ctrl+C", "url", "http://"+*listen, "login", adminUser+" / "+adminPassword)
	go func() {
		if err := http.ListenAndServe(*listen, srv.Handler()); err != nil {
			fail(err)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// --- Demo-Umgebung -----------------------------------------------------------

// hop ist ein Knoten auf dem Weg zur Gegenstelle.
type hop struct {
	ip, host, city, country, cc string
	lat, lon                    float64
	asn                         int64
	org                         string
	rtt                         float64
	silent                      bool // antwortet nicht (* * *)
	estimated                   bool // Position von Nachbarn übernommen
}

// profile beschreibt eine Gegenstelle und ihr Verhalten.
type profile struct {
	settings       model.ServerSettings
	down, up       float64 // typischer Durchsatz in Mbit/s
	eveningDrop    float64 // Anteil, der abends (19–23 Uhr) wegfällt
	jitter, loss   float64 // nur UDP
	failNight      int     // Tage zurück, an denen die Gegenstelle nachts ausfiel (0 = nie)
	route, detour  []hop   // aktueller Weg und früherer Umweg (optional)
	detourDaysBack int
}

var (
	gw   = hop{ip: "192.168.10.1", host: "gateway.office.example.net", rtt: 0.6, estimated: true, city: "Wolfsburg", country: "Germany", cc: "DE", lat: 52.4227, lon: 10.7865}
	isp  = hop{ip: "203.0.113.1", host: "bras1.wob.example-fiber.net", city: "Wolfsburg", country: "Germany", cc: "DE", lat: 52.4227, lon: 10.7865, asn: 64500, org: "Example Fiber", rtt: 2.1}
	hann = hop{ip: "203.0.113.9", host: "ae2.cr1.han.example-fiber.net", city: "Hanover", country: "Germany", cc: "DE", lat: 52.3759, lon: 9.7320, asn: 64500, org: "Example Fiber", rtt: 3.8}
	fra  = hop{ip: "198.51.100.1", host: "ae5.core1.fra.example-transit.net", city: "Frankfurt am Main", country: "Germany", cc: "DE", lat: 50.1109, lon: 8.6821, asn: 64501, org: "Example Transit", rtt: 8.4}
	fraX = hop{ip: "198.51.100.20", host: "decix-fra.example-transit.net", city: "Frankfurt am Main", country: "Germany", cc: "DE", lat: 50.1200, lon: 8.7350, asn: 64501, org: "Example Transit", rtt: 8.9}
	dus  = hop{ip: "198.51.100.33", host: "ae1.core2.dus.example-transit.net", city: "Düsseldorf", country: "Germany", cc: "DE", lat: 51.2277, lon: 6.7735, asn: 64501, org: "Example Transit", rtt: 11.2}
	amsX = hop{ip: "198.51.100.40", host: "amsix.example-carrier.net", city: "Amsterdam", country: "Netherlands", cc: "NL", lat: 52.3676, lon: 4.9041, asn: 64502, org: "Example Carrier", rtt: 14.6}
	par  = hop{ip: "198.51.100.51", host: "ae3.core1.par.example-carrier.net", city: "Paris", country: "France", cc: "FR", lat: 48.8566, lon: 2.3522, asn: 64502, org: "Example Carrier", rtt: 17.9}
	lon  = hop{ip: "198.51.100.60", host: "linx.example-carrier.net", city: "London", country: "United Kingdom", cc: "GB", lat: 51.5074, lon: -0.1278, asn: 64502, org: "Example Carrier", rtt: 21.7}
	ham  = hop{ip: "203.0.113.30", host: "ae4.cr1.ham.example-fiber.net", city: "Hamburg", country: "Germany", cc: "DE", lat: 53.5511, lon: 9.9937, asn: 64500, org: "Example Fiber", rtt: 5.9}
	dark = hop{silent: true}
)

func profiles() []profile {
	srv := func(name, host, desc string, proto model.Protocol, dir model.Direction, streams, interval int, autoTrace bool) model.ServerSettings {
		s := model.DefaultServerSettings()
		s.Name, s.Host, s.Description = name, host, &desc
		s.DefaultProtocol, s.DefaultDirection = proto, dir
		s.DefaultParallel, s.DefaultNumStreams = streams, streams
		s.ScheduleEnabled, s.ScheduleIntervalMinutes, s.AutoTraceEnabled = true, interval, autoTrace
		return s
	}
	target := func(ip, host, city, country, cc string, lat, lon float64, asn int64, org string, rtt float64) hop {
		return hop{ip: ip, host: host, city: city, country: country, cc: cc, lat: lat, lon: lon, asn: asn, org: org, rtt: rtt}
	}
	udp := srv("Hamburg (UDP)", "iperf.ham.example.net", "UDP quality for voice and video: jitter and packet loss", model.ProtocolUDP, model.DirectionBidirectional, 1, 60, false)
	bw := 500.0
	udp.DefaultUDPBandwidthMbps = &bw
	return []profile{
		{settings: srv("Frankfurt (DE-CIX)", "iperf.fra.example.net", "Data center uplink, 1 Gbit/s", model.ProtocolTCP, model.DirectionBidirectional, 4, 60, true),
			down: 930, up: 470, route: []hop{gw, isp, hann, fra, fraX, target("192.0.2.10", "iperf.fra.example.net", "Frankfurt am Main", "Germany", "DE", 50.1052, 8.6648, 64510, "Example Hosting", 9.3)}},
		{settings: srv("Amsterdam (AMS-IX)", "iperf.ams.example.net", "Cloud region eu-west", model.ProtocolTCP, model.DirectionDownload, 4, 60, true),
			down: 880, up: 0, eveningDrop: .58,
			route:          []hop{gw, isp, hann, fra, dus, amsX, target("192.0.2.20", "iperf.ams.example.net", "Amsterdam", "Netherlands", "NL", 52.3508, 4.9192, 64511, "Example Cloud", 15.2)},
			detour:         []hop{gw, isp, hann, fra, dark, par, lon, amsX, target("192.0.2.20", "iperf.ams.example.net", "Amsterdam", "Netherlands", "NL", 52.3508, 4.9192, 64511, "Example Cloud", 31.8)},
			detourDaysBack: 9},
		{settings: srv("London (LINX)", "iperf.lon.example.net", "Partner network", model.ProtocolTCP, model.DirectionBidirectional, 2, 120, true),
			down: 610, up: 380, route: []hop{gw, isp, hann, fra, par, lon, target("192.0.2.30", "iperf.lon.example.net", "London", "United Kingdom", "GB", 51.5155, -0.0922, 64510, "Example Hosting", 22.4)}},
		{settings: srv("Branch office Berlin (VPN)", "10.40.0.10", "Site-to-site VPN, 100 Mbit/s contract", model.ProtocolTCP, model.DirectionBidirectional, 1, 30, true),
			down: 96, up: 94, failNight: 6,
			route: []hop{gw, {ip: "10.40.0.1", host: "vpn-gw.berlin.example.net", city: "Berlin", country: "Germany", cc: "DE", lat: 52.5200, lon: 13.4050, rtt: 7.8, estimated: true},
				{ip: "10.40.0.10", host: "iperf.berlin.example.net", city: "Berlin", country: "Germany", cc: "DE", lat: 52.5200, lon: 13.4050, rtt: 8.1, estimated: true}}},
		{settings: udp, down: 492, up: 488, jitter: .9, loss: .05,
			route: []hop{gw, isp, hann, ham, target("192.0.2.40", "iperf.ham.example.net", "Hamburg", "Germany", "DE", 53.5488, 9.9872, 64510, "Example Hosting", 6.4)}},
	}
}

type demo struct {
	st   *store.Store
	conn *sql.DB
	rnd  *rand.Rand
}

func (d *demo) jitter(spread float64) float64 { return 1 + (d.rnd.Float64()*2-1)*spread }

// seedServer legt den Server an und erzeugt seine Tests im Intervall.
func (d *demo) seedServer(ctx context.Context, p profile, start, end time.Time) (int, error) {
	sv := &model.Server{ServerSettings: p.settings}
	if err := d.st.CreateServer(ctx, sv); err != nil {
		return 0, err
	}
	step := time.Duration(p.settings.ScheduleIntervalMinutes) * time.Minute
	count := 0
	var last *model.Test
	for t := start.Truncate(step).Add(step); !t.After(end); t = t.Add(step) {
		test := model.NewTestFromDefaults(sv)
		test.Duration = 10
		if err := d.st.CreateTest(ctx, test); err != nil {
			return count, err
		}
		begun := t.Add(time.Duration(d.rnd.IntN(20)) * time.Second)
		done := begun.Add(11 * time.Second)
		status := model.StatusCompleted
		var res model.TestResult
		var failure error
		age := end.Sub(t)
		if p.failNight > 0 && age > time.Duration(p.failNight)*24*time.Hour && age < time.Duration(p.failNight)*24*time.Hour+3*time.Hour {
			status = model.StatusFailed
			// What iperf3 itself reports; it has no translation.
			failure = i18n.Foreign("unable to connect to server: Connection timed out")
		} else {
			res = d.result(p, test, t)
		}
		if err := d.st.FinishTest(ctx, test.ID, status, done, res, failure, nil); err != nil {
			return count, err
		}
		if _, err := d.conn.ExecContext(ctx, `UPDATE tests SET created_at = ?, started_at = ?, completed_at = ? WHERE id = ?`,
			db.FormatTime(t), db.FormatTime(begun), db.FormatTime(done), test.ID); err != nil {
			return count, err
		}
		test.CreatedAt = t
		if status == model.StatusCompleted {
			last = test
		}
		// Ein Trace aus der Zeit des früheren Umwegs zeigt den Routenwechsel.
		if p.detour != nil && age > time.Duration(p.detourDaysBack)*24*time.Hour && age <= time.Duration(p.detourDaysBack)*24*time.Hour+step {
			if err := d.addTrace(ctx, test, p.settings.Host, p.detour, done); err != nil {
				return count, err
			}
		}
		count++
	}
	if last != nil && p.settings.AutoTraceEnabled || last != nil && p.settings.DefaultProtocol == model.ProtocolUDP {
		if err := d.addTrace(ctx, last, p.settings.Host, p.route, last.CreatedAt.Add(12*time.Second)); err != nil {
			return count, err
		}
	}
	return count, nil
}

// result liefert plausible Messwerte: tagsüber nahe am Normalwert, abends
// mit optionalem Peering-Engpass, gelegentlich ein Ausreißer.
func (d *demo) result(p profile, test *model.Test, t time.Time) model.TestResult {
	local := t.Local()
	h := float64(local.Hour()) + float64(local.Minute())/60
	load := 1.0
	if p.eveningDrop > 0 && h >= 19 && h < 23.5 {
		// Glocke um 21 Uhr: so sieht ein überlasteter Austauschknoten aus.
		load -= p.eveningDrop * math.Exp(-math.Pow((h-21.2)/1.3, 2))
	}
	if d.rnd.Float64() < .03 {
		load *= .55 + d.rnd.Float64()*.25
	}
	f := func(v float64) *float64 { v = math.Round(v*100) / 100; return &v }
	i := func(v int64) *int64 { return &v }
	var r model.TestResult
	dir := test.Direction
	if dir == model.DirectionDownload || dir == model.DirectionBidirectional {
		r.DownloadBandwidthMbps = f(p.down * load * d.jitter(.05))
		r.DownloadBytes = i(int64(*r.DownloadBandwidthMbps * 1e6 / 8 * float64(test.Duration)))
	}
	if (dir == model.DirectionUpload || dir == model.DirectionBidirectional) && p.up > 0 {
		r.UploadBandwidthMbps = f(p.up * math.Min(1, load+.2) * d.jitter(.05))
		r.UploadBytes = i(int64(*r.UploadBandwidthMbps * 1e6 / 8 * float64(test.Duration)))
	}
	if test.Protocol == model.ProtocolUDP {
		peak := 1.0
		if h >= 9 && h < 17 {
			peak = 1.6
		}
		r.DownloadJitterMs = f(p.jitter * peak * d.jitter(.35))
		r.UploadJitterMs = f(p.jitter * peak * d.jitter(.35))
		r.DownloadPacketLossPercent = f(math.Max(0, p.loss*peak*d.jitter(.9)))
		r.UploadPacketLossPercent = f(math.Max(0, p.loss*peak*d.jitter(.9)))
	} else {
		retr := int64(d.rnd.IntN(40))
		if load < .7 {
			retr += int64(400 * (1 - load) * d.jitter(.3))
		}
		r.Retransmits = i(retr)
	}
	r.CPUPercent = f(4 + d.rnd.Float64()*9)
	return r
}

// addTrace speichert einen Traceroute zum Test mit leicht gestreuten Laufzeiten.
func (d *demo) addTrace(ctx context.Context, test *model.Test, host string, route []hop, at time.Time) error {
	str := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	tr := &model.Trace{TestID: &test.ID, DestinationHost: host, Completed: true, StartedAt: &at}
	src := "192.168.10.25"
	tr.SourceIP = &src
	doneAt := at.Add(9 * time.Second)
	tr.CompletedAt = &doneAt
	var total float64
	for n, h := range route {
		th := model.TraceHop{HopNumber: n + 1, Responded: !h.silent}
		if !h.silent {
			rtt := math.Round(h.rtt*d.jitter(.12)*100) / 100
			total = rtt
			loss := 0.0
			th.IPAddress, th.Hostname, th.RTTMs, th.PacketLoss = str(h.ip), str(h.host), &rtt, &loss
			if h.lat != 0 || h.lon != 0 {
				lat, lon := h.lat, h.lon
				th.Latitude, th.Longitude = &lat, &lon
				th.City, th.Country, th.CountryCode = str(h.city), str(h.country), str(h.cc)
				th.GeoIPInterpolated = h.estimated
			}
			if h.asn != 0 {
				asn := h.asn
				th.ASN, th.ASNOrganization = &asn, str(h.org)
			}
		} else {
			loss := 100.0
			th.PacketLoss = &loss
		}
		tr.Hops = append(tr.Hops, th)
	}
	last := route[len(route)-1]
	tr.DestinationIP = str(last.ip)
	tr.TotalRTTMs = &total
	if err := d.st.CreateTrace(ctx, tr); err != nil {
		return err
	}
	_, err := d.conn.ExecContext(ctx, `UPDATE traces SET created_at = ? WHERE id = ?`, db.FormatTime(at), tr.ID)
	return err
}
