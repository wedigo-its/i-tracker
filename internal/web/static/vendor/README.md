# Embedded third-party libraries

The interface loads nothing from foreign servers at runtime (CSP
`default-src 'self'`). These files ship embedded via `go:embed`.
Library files are taken unmodified from the npm packages.

| File | Version | Source | License | SHA-256 |
|---|---|---|---|---|
| chartjs/chart.umd.js | Chart.js 4.4.1 | registry.npmjs.org/chart.js/-/chart.js-4.4.1.tgz (`dist/chart.umd.js`) | MIT (chartjs/LICENSE.md) | 74401d738dd3e03ee5dfb3b6841210fe2c4ead8a960c4011ca4ba0b78a9fd8f3 |
| leaflet/leaflet.js | Leaflet 1.9.4 | registry.npmjs.org/leaflet/-/leaflet-1.9.4.tgz (`dist/leaflet.js`) | BSD-2-Clause (leaflet/LICENSE) | db49d009c841f5ca34a888c96511ae936fd9f5533e90d8b2c4d57596f4e5641a |
| leaflet/leaflet.css, leaflet/images/* | Leaflet 1.9.4 | as above (`dist/leaflet.css`, `dist/images/`) | BSD-2-Clause | a7837102824184820dfa198d1ebcd109ff6d0ff9a2672a074b9a1b4d147d04c6 (css) |
| naturalearth/world-110m.geojson | Natural Earth 5.1.2, Admin 0 Countries 1:110m | github.com/nvkelso/natural-earth-vector (`geojson/ne_110m_admin_0_countries.geojson`) | public domain (naturalearth/LICENSE.txt) | – (modified: geometry and name only, coordinates rounded to 2 decimal places) |

Updating: download the new npm package, replace the files, record version and hash in
this table and run `go test ./internal/web/`.
