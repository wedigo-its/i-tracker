# i-Tracker as a container: one Go binary with an embedded web interface.
# Build and start: docker compose up -d --build

# ---------- Build ----------
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG BUILD_DATE=docker
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X 'i-tracker/internal/version.BuildDate=${BUILD_DATE}'" \
      -o /out/i-tracker ./cmd/i-tracker

# ---------- Runtime ----------
# trixie ships iperf3 3.18; at least 3.17 is required (--json-stream).
# bookworm only has 3.12 and is therefore not an option.
FROM debian:trixie-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      iperf3 traceroute ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/i-tracker /app/i-tracker

# Configuration and database live in the /data volume; config.yaml is created
# with a random token key on first start. The initial admin password is
# printed once to the container log (docker logs i-tracker) and must be
# changed at the first sign-in. The GeoIP database is downloaded on first start
# (DB-IP City Lite, or MaxMind GeoLite2 with IPERF3_GEOIP_MAXMIND_*) into /data.
# Environment variables follow IPERF3_<SECTION>_<KEY>. The container listens
# on all interfaces (the built-in fallback without config is 127.0.0.1:8000).
ENV IPERF3_WEB_LISTEN=0.0.0.0:8000 \
    IPERF3_STORAGE_PATH=/data/i-tracker.db \
    IPERF3_GEOIP_PATH=/data/geoip/city.mmdb \
    TZ=Europe/Berlin
VOLUME /data
EXPOSE 8000
ENTRYPOINT ["/app/i-tracker", "-config", "/data/config.yaml"]
