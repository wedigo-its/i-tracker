package iperf

import (
	"encoding/json"
	"math"

	"i-tracker/internal/model"
)

// `iperf3 --json-stream` output (iperf 3.17+): one JSON line per event
// ("start", "interval", "end", "error").
//
// Directions are client-side: without -R the client sends (upload), with -R the
// server sends (download). With --bidir, sum/sum_sent/sum_received describe the
// upload and the *_bidir_reverse fields the download.

type event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// summary is a sum over all streams of one direction.
type summary struct {
	Bytes         int64    `json:"bytes"`
	BitsPerSecond float64  `json:"bits_per_second"`
	Retransmits   *int64   `json:"retransmits"` // TCP sender only, not on all platforms
	JitterMs      *float64 `json:"jitter_ms"`   // UDP only
	LostPercent   *float64 `json:"lost_percent"`
}

type intervalData struct {
	Sum             *summary `json:"sum"`
	SumBidirReverse *summary `json:"sum_bidir_reverse"`
}

type endData struct {
	SumSent                 *summary `json:"sum_sent"`
	SumReceived             *summary `json:"sum_received"`
	SumSentBidirReverse     *summary `json:"sum_sent_bidir_reverse"`
	SumReceivedBidirReverse *summary `json:"sum_received_bidir_reverse"`
	CPU                     *struct {
		HostTotal float64 `json:"host_total"`
	} `json:"cpu_utilization_percent"`
}

// Rates are an interval's current bit rates in Mbit/s; nil = not reported.
type Rates struct {
	DownloadMbps *float64
	UploadMbps   *float64
}

// intervalRates maps an interval event to download and upload.
func intervalRates(d intervalData, dir model.Direction) Rates {
	var r Rates
	switch dir {
	case model.DirectionDownload:
		r.DownloadMbps = mbps(d.Sum)
	case model.DirectionUpload:
		r.UploadMbps = mbps(d.Sum)
	case model.DirectionBidirectional:
		r.UploadMbps = mbps(d.Sum)
		r.DownloadMbps = mbps(d.SumBidirReverse)
	}
	return r
}

// endResult converts the end event into results; ok is false if the requested
// direction has no sums.
func endResult(d endData, dir model.Direction) (r model.TestResult, ok bool) {
	var retrans []*int64
	switch dir {
	case model.DirectionDownload:
		ok = setFlow(&r.DownloadBandwidthMbps, &r.DownloadBytes, &r.DownloadJitterMs, &r.DownloadPacketLossPercent, d.SumSent, d.SumReceived)
		retrans = append(retrans, retransmits(d.SumSent))
	case model.DirectionUpload:
		ok = setFlow(&r.UploadBandwidthMbps, &r.UploadBytes, &r.UploadJitterMs, &r.UploadPacketLossPercent, d.SumSent, d.SumReceived)
		retrans = append(retrans, retransmits(d.SumSent))
	case model.DirectionBidirectional:
		up := setFlow(&r.UploadBandwidthMbps, &r.UploadBytes, &r.UploadJitterMs, &r.UploadPacketLossPercent, d.SumSent, d.SumReceived)
		down := setFlow(&r.DownloadBandwidthMbps, &r.DownloadBytes, &r.DownloadJitterMs, &r.DownloadPacketLossPercent, d.SumSentBidirReverse, d.SumReceivedBidirReverse)
		ok = up && down
		retrans = append(retrans, retransmits(d.SumSent), retransmits(d.SumSentBidirReverse))
	}
	for _, n := range retrans {
		if n != nil {
			total := *n
			if r.Retransmits != nil {
				total += *r.Retransmits
			}
			r.Retransmits = &total
		}
	}
	if d.CPU != nil {
		cpu := round2(d.CPU.HostTotal)
		r.CPUPercent = &cpu
	}
	return r, ok
}

// setFlow sets one direction's values, preferring the receiver side (the
// volume actually transferred).
func setFlow(bw **float64, bytes **int64, jitter, loss **float64, sent, received *summary) bool {
	s := received
	if s == nil {
		s = sent
	}
	if s == nil {
		return false
	}
	*bw = mbps(s)
	b := s.Bytes
	*bytes = &b
	if s.JitterMs != nil {
		j := round2(*s.JitterMs)
		*jitter = &j
	}
	if s.LostPercent != nil {
		l := round2(*s.LostPercent)
		*loss = &l
	}
	return true
}

func retransmits(s *summary) *int64 {
	if s == nil {
		return nil
	}
	return s.Retransmits
}

func mbps(s *summary) *float64 {
	if s == nil {
		return nil
	}
	v := round2(s.BitsPerSecond / 1e6)
	return &v
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
