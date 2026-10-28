package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"i-tracker/internal/i18n"
	"i-tracker/internal/iperf"
	"i-tracker/internal/model"
)

// cleanupBefore reads days (≥ 0) and all. Without days and all the caller
// answers with an error, so nothing is deleted by accident.
func cleanupBefore(r *http.Request) (before *time.Time, all bool, err error) {
	days, err := queryInt64(r, "days")
	if err != nil {
		return nil, false, err
	}
	if days != nil {
		if *days < 0 {
			return nil, false, i18n.E("err.param.negative", "days")
		}
		t := time.Now().AddDate(0, 0, -int(*days))
		before = &t
	}
	allParam, err := queryBool(r, "all")
	if err != nil {
		return nil, false, err
	}
	return before, allParam != nil && *allParam, nil
}

func (s *Server) handleCleanupTests(w http.ResponseWriter, r *http.Request, _ *model.User) {
	before, all, err := cleanupBefore(r)
	if err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	serverID, err := queryInt64(r, "server_id")
	if err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if !all && before == nil && serverID == nil {
		writeError(w, r, http.StatusBadRequest, "err.cleanup.tests_scope")
		return
	}
	n, err := s.store.DeleteTests(r.Context(), before, serverID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"message":       message(r, "msg.tests.deleted", n),
		"deleted_count": n,
	})
}

func (s *Server) handleCleanupTraces(w http.ResponseWriter, r *http.Request, _ *model.User) {
	before, all, err := cleanupBefore(r)
	if err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if !all && before == nil {
		writeError(w, r, http.StatusBadRequest, "err.cleanup.traces_scope")
		return
	}
	if all {
		before = nil
	}
	traces, hops, err := s.store.DeleteTraces(r.Context(), before)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"message":        message(r, "msg.traces.deleted", traces, hops),
		"deleted_traces": traces,
		"deleted_hops":   hops,
	})
}

func (s *Server) handleDatabaseStats(w http.ResponseWriter, r *http.Request, _ *model.User) {
	st, err := s.store.DatabaseStats(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, st)
}

// publicServer is an entry of the curated list; name, location and
// description are messages, rendered in the request language.
type publicServer struct {
	name, location, description i18n.Msg
	host, provider              string
	port                        int
}

// render returns the entry in lang.
func (p publicServer) render(lang string) model.PublicServer {
	return model.PublicServer{
		Name: p.name.In(lang), Host: p.host, Port: p.port, Provider: p.provider,
		Location: p.location.In(lang), Description: p.description.In(lang),
	}
}

// publicServers is a curated list of public iperf3 servers; they are not
// guaranteed to be reachable.
var publicServers = []publicServer{
	{name: i18n.Raw("Bouygues Telecom Paris"), host: "iperf.par2.as5410.net", port: 5200, location: i18n.M("public.loc.paris"), provider: "Bouygues Telecom", description: i18n.M("public.desc.in", "Paris")},
	{name: i18n.Raw("OVH Proof"), host: "proof.ovh.net", port: 5201, location: i18n.M("public.loc.france"), provider: "OVH", description: i18n.M("public.desc.test_by", "OVH")},
	{name: i18n.Raw("Online.net Paris"), host: "ping.online.net", port: 5200, location: i18n.M("public.loc.paris"), provider: "Online.net", description: i18n.M("public.desc.by", "Online.net")},
	{name: i18n.Raw("wilhelm.tel Hamburg"), host: "speedtest.wtnet.de", port: 5200, location: i18n.M("public.loc.hamburg"), provider: "wilhelm.tel", description: i18n.M("public.desc.by", "wilhelm.tel")},
	{name: i18n.Raw("Speedtest Frankfurt"), host: "speedtest.fra.de.as9136.net", port: 5200, location: i18n.M("public.loc.frankfurt"), provider: "AS9136", description: i18n.M("public.desc.in", "Frankfurt")},
	{name: i18n.Raw("Wobcom Wolfsburg"), host: "a400.speedtest.wobcom.de", port: 5201, location: i18n.M("public.loc.wolfsburg"), provider: "WOBCOM", description: i18n.M("public.desc.by", "WOBCOM (AS9136)")},
	{name: i18n.M("public.name.uztelecom"), host: "speedtest.uztelecom.uz", port: 5200, location: i18n.M("public.loc.tashkent"), provider: "Uztelecom", description: i18n.M("public.desc.by", "Uztelecom")},
	{name: i18n.Raw("AT&T Ashburn VA"), host: "speedtest.ashb.va.us.as7018.net", port: 5201, location: i18n.Raw("Ashburn, Virginia, USA"), provider: "AT&T", description: i18n.M("public.desc.by_in", "AT&T", "Virginia")},
	{name: i18n.Raw("Hurricane Electric"), host: "speedtest.dal.tx.us.he.net", port: 5201, location: i18n.Raw("Dallas, Texas, USA"), provider: "Hurricane Electric", description: i18n.M("public.desc.server_by_in", "Hurricane Electric", "Dallas")},
	{name: i18n.Raw("Vodafone Portugal"), host: "speedtest.vodafone.pt", port: 5201, location: i18n.Raw("Portugal"), provider: "Vodafone", description: i18n.M("public.desc.by", "Vodafone Portugal")},
	{name: i18n.M("public.name.init7"), host: "speedtest.init7.net", port: 5201, location: i18n.M("public.loc.switzerland"), provider: "Init7", description: i18n.M("public.desc.by", "Init7")},
}

func (s *Server) handlePublicServers(w http.ResponseWriter, r *http.Request) {
	lang := i18n.FromRequest(r)
	out := make([]model.PublicServer, len(publicServers))
	for i, p := range publicServers {
		out[i] = p.render(lang)
	}
	writeJSON(w, r, http.StatusOK, out)
}

// handleSearchPublicServers searches name and location (all languages) and provider, ignoring case.
func (s *Server) handleSearchPublicServers(w http.ResponseWriter, r *http.Request) {
	q, ok := r.URL.Query()["query"]
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "err.param.missing", "query")
		return
	}
	needle := strings.ToLower(q[0])
	lang := i18n.FromRequest(r)
	results := []model.PublicServer{}
	for _, p := range publicServers {
		haystack := []string{p.provider}
		for _, l := range i18n.Languages() {
			haystack = append(haystack, p.name.In(l), p.location.In(l))
		}
		for _, h := range haystack {
			if strings.Contains(strings.ToLower(h), needle) {
				results = append(results, p.render(lang))
				break
			}
		}
	}
	writeJSON(w, r, http.StatusOK, results)
}

// handleInstallIperf starts the installation of iperf3 through the package
// manager after an administrator confirmed it in the UI.
func (s *Server) handleInstallIperf(w http.ResponseWriter, r *http.Request, u *model.User) {
	switch err := s.runner.InstallAsync(); {
	case errors.Is(err, iperf.ErrInstallRunning):
		writeErr(w, r, http.StatusConflict, err)
	case errors.Is(err, iperf.ErrNotInstallable):
		if st := s.runner.Status(); st.Err != nil {
			err = i18n.E("err.with_cause", err, st.Err)
		}
		writeErr(w, r, http.StatusConflict, err)
	case err != nil:
		writeInternal(w, r, err)
	default:
		slog.Info("iperf3 installation requested", "user", u.Username)
		writeJSON(w, r, http.StatusAccepted, s.iperfStatus(r))
	}
}
