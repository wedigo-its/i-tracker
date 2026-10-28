// Package model enthält die Datenstrukturen der Anwendung. JSON-Felder sind
// in snake_case benannt; optionale Felder sind Pointer und werden als null serialisiert.
package model

import (
	"strconv"
	"time"

	"i-tracker/internal/i18n"
)

// User ist ein Benutzerkonto.
type User struct {
	ID             int64      `json:"id"`
	Username       string     `json:"username"`
	Email          string     `json:"email"`
	HashedPassword string     `json:"-"`
	IsActive       bool       `json:"is_active"`
	IsAdmin        bool       `json:"is_admin"`
	CreatedAt      time.Time  `json:"created_at"`
	LastLogin      *time.Time `json:"last_login"`
	// MustChangePassword erzwingt einen Passwortwechsel vor jeder weiteren Nutzung.
	MustChangePassword bool `json:"must_change_password"`
	// TokenVersion steht in jedem Sitzungs-Token; ist sie dort kleiner, ist das
	// Token ungültig (nach Passwortwechsel oder „überall abmelden“).
	TokenVersion int64 `json:"-"`
}

// Protocol ist das Transportprotokoll eines iperf3-Tests.
type Protocol string

const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

// Valid meldet, ob p ein bekannter Wert ist.
func (p Protocol) Valid() bool { return p == ProtocolTCP || p == ProtocolUDP }

// Direction ist die Messrichtung eines iperf3-Tests.
type Direction string

const (
	DirectionDownload      Direction = "download"
	DirectionUpload        Direction = "upload"
	DirectionBidirectional Direction = "bidirectional"
)

// Valid meldet, ob d ein bekannter Wert ist.
func (d Direction) Valid() bool {
	return d == DirectionDownload || d == DirectionUpload || d == DirectionBidirectional
}

// ServerSettings sind die vom Benutzer pflegbaren Felder eines Server-Profils.
// Requests werden nur in diesen Teil dekodiert, sodass ID und Zeitstempel
// nicht von außen überschrieben werden können.
type ServerSettings struct {
	Name              string    `json:"name"`
	Host              string    `json:"host"`
	Port              int       `json:"port"`
	Description       *string   `json:"description"`
	Enabled           bool      `json:"enabled"`
	DefaultDuration   int       `json:"default_duration"`
	DefaultParallel   int       `json:"default_parallel"`
	DefaultNumStreams int       `json:"default_num_streams"`
	DefaultProtocol   Protocol  `json:"default_protocol"`
	DefaultDirection  Direction `json:"default_direction"`
	// DefaultUDPBandwidthMbps ist die Zielbandbreite für UDP-Tests (iperf3 -b);
	// nil = iperf3-Standard von 1 Mbit/s.
	DefaultUDPBandwidthMbps *float64 `json:"default_udp_bandwidth_mbps"`
	ScheduleEnabled         bool     `json:"schedule_enabled"`
	ScheduleIntervalMinutes int      `json:"schedule_interval_minutes"`
	AutoTraceEnabled        bool     `json:"auto_trace_enabled"`
}

// DefaultServerSettings liefert die Vorgabewerte für ein neues Server-Profil.
func DefaultServerSettings() ServerSettings {
	return ServerSettings{
		Port:                    5201,
		Enabled:                 true,
		DefaultDuration:         10,
		DefaultParallel:         1,
		DefaultNumStreams:       1,
		DefaultProtocol:         ProtocolTCP,
		DefaultDirection:        DirectionDownload,
		ScheduleIntervalMinutes: 30,
	}
}

// Validate checks the value ranges; the error (an *i18n.Error) names the
// first invalid field.
func (s ServerSettings) Validate() error {
	inRange := func(v, lo, hi int) bool { return v >= lo && v <= hi }
	switch {
	case !inRange(len([]rune(s.Name)), 1, 100):
		return i18n.E("err.input.name_length")
	case !inRange(len([]rune(s.Host)), 1, 255):
		return i18n.E("err.input.host_length")
	case !inRange(s.Port, 1, 65535):
		return i18n.E("err.input.port")
	case !inRange(s.DefaultDuration, 1, 300):
		return i18n.E("err.input.duration")
	case !inRange(s.DefaultParallel, 1, 128):
		return i18n.E("err.input.parallel")
	case !inRange(s.DefaultNumStreams, 1, 128):
		return i18n.E("err.input.streams")
	case !s.DefaultProtocol.Valid():
		return i18n.E("err.input.protocol")
	case !s.DefaultDirection.Valid():
		return i18n.E("err.input.direction")
	case s.ScheduleIntervalMinutes < 1:
		return i18n.E("err.input.interval")
	}
	return ValidateUDPBandwidth(s.DefaultUDPBandwidthMbps)
}

// MaxUDPBandwidthMbps ist die höchste zulässige UDP-Zielbandbreite (100 Gbit/s).
const MaxUDPBandwidthMbps = 100000

// ValidateUDPBandwidth checks an optional UDP target bandwidth. 0 is not
// allowed: iperf3 takes it as "unlimited" and floods the line.
func ValidateUDPBandwidth(v *float64) error {
	if v != nil && (*v <= 0 || *v > MaxUDPBandwidthMbps) {
		return i18n.E("err.input.udp_bandwidth", strconv.Itoa(MaxUDPBandwidthMbps))
	}
	return nil
}

// TestStatus ist der Ausführungsstatus eines Tests.
type TestStatus string

const (
	StatusPending   TestStatus = "pending"
	StatusRunning   TestStatus = "running"
	StatusCompleted TestStatus = "completed"
	StatusFailed    TestStatus = "failed"
)

// Valid meldet, ob s ein bekannter Wert ist.
func (s TestStatus) Valid() bool {
	return s == StatusPending || s == StatusRunning || s == StatusCompleted || s == StatusFailed
}

// TestResult sind die Messwerte eines abgeschlossenen Tests.
type TestResult struct {
	DownloadBandwidthMbps     *float64 `json:"download_bandwidth_mbps"`
	DownloadBytes             *int64   `json:"download_bytes"`
	DownloadJitterMs          *float64 `json:"download_jitter_ms"`
	DownloadPacketLossPercent *float64 `json:"download_packet_loss_percent"`
	UploadBandwidthMbps       *float64 `json:"upload_bandwidth_mbps"`
	UploadBytes               *int64   `json:"upload_bytes"`
	UploadJitterMs            *float64 `json:"upload_jitter_ms"`
	UploadPacketLossPercent   *float64 `json:"upload_packet_loss_percent"`
	Retransmits               *int64   `json:"retransmits"`
	CPUPercent                *float64 `json:"cpu_percent"`
}

// Test ist ein iperf3-Testlauf. Die Rohausgabe wird nur in der Detailansicht
// geliefert (siehe TestDetail).
type Test struct {
	ID              int64     `json:"id"`
	ServerID        int64     `json:"server_id"`
	Protocol        Protocol  `json:"protocol"`
	Direction       Direction `json:"direction"`
	Duration        int       `json:"duration"`
	ParallelStreams int       `json:"parallel_streams"`
	// UDPBandwidthMbps ist die Zielbandbreite, mit der ein UDP-Test lief;
	// nil bei TCP oder iperf3-Standard.
	UDPBandwidthMbps *float64   `json:"udp_bandwidth_mbps"`
	Status           TestStatus `json:"status"`
	StartedAt        *time.Time `json:"started_at"`
	CompletedAt      *time.Time `json:"completed_at"`
	TestResult
	// ErrorMessage is the reason of a failure in English (in older rows
	// German); the web layer renders it in the request language with
	// ErrorText, its language-neutral form (i18n.Msg, encoded).
	ErrorMessage *string   `json:"error_message"`
	ErrorText    string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// NewTestFromDefaults erstellt einen Test mit den Vorgaben des Servers
// (für geplante Tests).
func NewTestFromDefaults(sv *Server) *Test {
	t := &Test{
		ServerID:        sv.ID,
		Protocol:        sv.DefaultProtocol,
		Direction:       sv.DefaultDirection,
		Duration:        sv.DefaultDuration,
		ParallelStreams: sv.DefaultParallel,
	}
	if t.Protocol == ProtocolUDP {
		t.UDPBandwidthMbps = sv.DefaultUDPBandwidthMbps
	}
	return t
}

// TestDetail ist ein Test inklusive Server-Profil und Rohausgabe.
type TestDetail struct {
	Test
	Server    Server  `json:"server"`
	RawOutput *string `json:"raw_output"`
}

// TraceHop ist ein Hop einer Routenverfolgung inklusive Standortdaten.
type TraceHop struct {
	ID              int64    `json:"id"`
	HopNumber       int      `json:"hop_number"`
	IPAddress       *string  `json:"ip_address"`
	Hostname        *string  `json:"hostname"`
	Latitude        *float64 `json:"latitude"`
	Longitude       *float64 `json:"longitude"`
	City            *string  `json:"city"`
	Country         *string  `json:"country"`
	CountryCode     *string  `json:"country_code"`
	ASN             *int64   `json:"asn"`
	ASNOrganization *string  `json:"asn_organization"`
	RTTMs           *float64 `json:"rtt_ms"`
	PacketLoss      *float64 `json:"packet_loss"`
	Responded       bool     `json:"responded"`
	// GeoIPInterpolated ist true, wenn die Koordinaten von einem Nachbar-Hop
	// übernommen wurden, weil die GeoIP-Datenbank die Adresse nicht kennt.
	GeoIPInterpolated bool `json:"geoip_interpolated"`
}

// SetError records err as the reason the trace is incomplete: the English
// text and its language-neutral form.
func (t *Trace) SetError(err error) {
	msg := err.Error()
	t.ErrorMessage = &msg
	t.ErrorText = i18n.MsgOf(err).Encode()
}

// HasLocation meldet, ob der Hop Koordinaten hat.
func (h TraceHop) HasLocation() bool { return h.Latitude != nil && h.Longitude != nil }

// Trace ist eine Routenverfolgung, optional zu einem Test.
type Trace struct {
	ID              int64    `json:"id"`
	TestID          *int64   `json:"test_id"`
	SourceIP        *string  `json:"source_ip"`
	DestinationIP   *string  `json:"destination_ip"`
	DestinationHost string   `json:"destination_host"`
	TotalHops       int      `json:"total_hops"`
	TotalRTTMs      *float64 `json:"total_rtt_ms"`
	Completed       bool     `json:"completed"`
	// ErrorMessage and ErrorText as in Test.
	ErrorMessage *string    `json:"error_message"`
	ErrorText    string     `json:"-"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	Hops         []TraceHop `json:"hops"`
}

// DashboardStats sind die Kennzahlen der Übersicht.
type DashboardStats struct {
	TotalServers    int        `json:"total_servers"`
	ActiveServers   int        `json:"active_servers"`
	TotalTests      int        `json:"total_tests"`
	TestsToday      int        `json:"tests_today"`
	AvgDownloadMbps *float64   `json:"avg_download_mbps"`
	AvgUploadMbps   *float64   `json:"avg_upload_mbps"`
	LastTestAt      *time.Time `json:"last_test_at"`
}

// ServerStats sind die Kennzahlen eines Servers. Mittelwerte beziehen sich
// nur auf erfolgreiche Tests.
type ServerStats struct {
	ServerID             int64      `json:"server_id"`
	ServerName           string     `json:"server_name"`
	TotalTests           int        `json:"total_tests"`
	SuccessfulTests      int        `json:"successful_tests"`
	FailedTests          int        `json:"failed_tests"`
	AvgDownloadMbps      *float64   `json:"avg_download_mbps"`
	AvgUploadMbps        *float64   `json:"avg_upload_mbps"`
	AvgJitterMs          *float64   `json:"avg_jitter_ms"`
	AvgPacketLossPercent *float64   `json:"avg_packet_loss_percent"`
	LastTestAt           *time.Time `json:"last_test_at"`
}

// DatabaseStats sind die Datenbank-Kennzahlen der Administration.
type DatabaseStats struct {
	TotalTests   int        `json:"total_tests"`
	TotalServers int        `json:"total_servers"`
	TotalUsers   int        `json:"total_users"`
	TotalTraces  int        `json:"total_traces"`
	TotalHops    int        `json:"total_hops"`
	OldestTest   *time.Time `json:"oldest_test"`
	NewestTest   *time.Time `json:"newest_test"`
}

// PublicServer ist ein bekannter öffentlicher iperf3-Server.
type PublicServer struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Location    string `json:"location"`
	Provider    string `json:"provider"`
	Description string `json:"description"`
}

// Server ist ein iperf3-Server-Profil.
type Server struct {
	ID int64 `json:"id"`
	ServerSettings
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
