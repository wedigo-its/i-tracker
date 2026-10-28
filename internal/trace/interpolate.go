package trace

import (
	"math/rand/v2"

	"i-tracker/internal/model"
)

const (
	// sameSubnetRange is the search radius (hops) for a donor in the same /24.
	sameSubnetRange = 5
	// jitterDegrees randomly offsets interpolated points (about ±5 km) so they
	// do not sit exactly on the donor on the map.
	jitterDegrees = 0.05
)

// Interpolate fills coordinates for answered hops unknown to GeoIP: from a hop
// in the same /24 within ±5 hops, else the nearest located hop (backward
// first). Copied data is flagged GeoIPInterpolated and the city prefixed "? "
// (no city when the donor has none).
func Interpolate(hops []model.TraceHop, rnd *rand.Rand) {
	for i := range hops {
		h := &hops[i]
		if h.HasLocation() || !h.Responded || h.IPAddress == nil {
			continue
		}
		donor := findDonor(hops, i)
		if donor == nil {
			continue
		}
		lat := *donor.Latitude + (rnd.Float64()*2-1)*jitterDegrees
		lon := *donor.Longitude + (rnd.Float64()*2-1)*jitterDegrees
		// Without a known city the hop has none; the UI names it unknown in
		// the viewer's language.
		var city *string
		if donor.City != nil && *donor.City != "" {
			c := "? " + *donor.City
			city = &c
		}
		h.Latitude, h.Longitude, h.City = &lat, &lon, city
		h.Country, h.CountryCode = donor.Country, donor.CountryCode
		h.GeoIPInterpolated = true
	}
}

func findDonor(hops []model.TraceHop, i int) *model.TraceHop {
	subnet := subnet24(*hops[i].IPAddress)
	at := func(j int) *model.TraceHop {
		if j < 0 || j >= len(hops) || !hops[j].HasLocation() {
			return nil
		}
		return &hops[j]
	}
	if subnet != "" {
		for off := 1; off <= sameSubnetRange; off++ {
			for _, j := range []int{i - off, i + off} {
				if d := at(j); d != nil && d.IPAddress != nil && subnet24(*d.IPAddress) == subnet {
					return d
				}
			}
		}
	}
	for off := 1; off < len(hops); off++ {
		for _, j := range []int{i - off, i + off} {
			if d := at(j); d != nil {
				return d
			}
		}
	}
	return nil
}
