package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"i-tracker/internal/auth"
	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
	"i-tracker/internal/store"
)

// Session cookie: __Host- prefix under TLS only (requires Secure); both names are read.
const (
	sessionCookie       = "iperf3_session"
	sessionCookieSecure = "__Host-iperf3_session"
	csrfHeader          = "X-CSRF-Token"
)

// userHandler receives the signed-in user.
type userHandler func(w http.ResponseWriter, r *http.Request, u *model.User)

// requireUser requires a valid session and rejects users with a pending password change.
func (s *Server) requireUser(h userHandler) http.Handler {
	return s.withUser(h, false)
}

// requireUserPending also admits a pending password change (/auth/me, /auth/change-password).
func (s *Server) requireUserPending(h userHandler) http.Handler {
	return s.withUser(h, true)
}

func (s *Server) withUser(h userHandler, allowPending bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.authenticate(w, r, allowPending)
		if !ok {
			return
		}
		h(w, r, u)
	})
}

// optionalUser is for read-only GET routes: with auth.public_read it calls h
// with u == nil for guests (invalid sessions count as guests, no 401);
// otherwise it acts like requireUser. Mutating routes must use requireUser.
func (s *Server) optionalUser(h userHandler) http.Handler {
	if !s.publicRead {
		return s.requireUser(h)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, sess, err := s.userForRequest(r, false)
		switch {
		case err == nil:
			s.renewSession(w, r, sess)
		case errors.Is(err, errUnauthorized), errors.Is(err, errInactive),
			errors.Is(err, errMustChangePass), errors.Is(err, errCSRF):
			u = nil
		default:
			writeInternal(w, r, err)
			return
		}
		h(w, r, u)
	})
}

// requireAdmin requires an admin session.
func (s *Server) requireAdmin(h userHandler) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request, u *model.User) {
		if !u.IsAdmin {
			writeError(w, r, http.StatusForbidden, "err.auth.forbidden")
			return
		}
		h(w, r, u)
	})
}

var (
	errUnauthorized   = i18n.E("err.auth.unauthorized")
	errInactive       = i18n.E("err.auth.user_disabled")
	errCSRF           = i18n.E("err.auth.csrf")
	errMustChangePass = i18n.E("err.auth.password_change_required")
)

// session is a request's validated session.
type session struct {
	token      string
	claims     auth.Claims
	fromCookie bool
}

// authenticate loads the session's user; if !ok the error response is written.
// Cookie sessions slide (renewSession); Bearer tokens keep a fixed lifetime.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, allowPending bool) (u *model.User, ok bool) {
	u, sess, err := s.userForRequest(r, allowPending)
	if err == nil {
		s.renewSession(w, r, sess)
	}
	switch {
	case errors.Is(err, errUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErr(w, r, http.StatusUnauthorized, err)
	case errors.Is(err, errInactive):
		writeErr(w, r, http.StatusBadRequest, err)
	case errors.Is(err, errCSRF), errors.Is(err, errMustChangePass):
		writeErr(w, r, http.StatusForbidden, err)
	case err != nil:
		writeInternal(w, r, err)
	default:
		return u, true
	}
	return nil, false
}

// renewSession renews a cookie session past half its lifetime.
func (s *Server) renewSession(w http.ResponseWriter, r *http.Request, sess session) {
	if !sess.fromCookie || !s.tokens.NeedsRenewal(sess.claims) {
		return
	}
	if token, _, err := s.tokens.Renew(sess.claims); err == nil {
		setSessionCookie(w, r, token, s.tokens.TTL())
	}
}

// userForRequest checks session, CSRF token (cookie + unsafe method) and forced password change.
func (s *Server) userForRequest(r *http.Request, allowPending bool) (*model.User, session, error) {
	token, fromCookie := sessionToken(r)
	u, claims, err := s.userForToken(r.Context(), token)
	if err != nil {
		return nil, session{}, err
	}
	// Browsers send cookies automatically, never Bearer tokens: CSRF only applies to cookies.
	if fromCookie && !safeMethod(r.Method) && !s.tokens.CheckCSRF(claims.SessionID, r.Header.Get(csrfHeader)) {
		return nil, session{}, errCSRF
	}
	if u.MustChangePassword && !allowPending {
		return nil, session{}, errMustChangePass
	}
	return u, session{token: token, claims: claims, fromCookie: fromCookie}, nil
}

// userForToken loads the token's active user; revoked sessions and stale
// token versions yield errUnauthorized.
func (s *Server) userForToken(ctx context.Context, token string) (*model.User, auth.Claims, error) {
	if token == "" {
		return nil, auth.Claims{}, errUnauthorized
	}
	claims, err := s.tokens.Parse(token)
	if err != nil || s.revoked.Revoked(claims.SessionID) {
		return nil, auth.Claims{}, errUnauthorized
	}
	u, err := s.store.UserByUsername(ctx, claims.Username)
	if errors.Is(err, store.ErrNotFound) {
		return nil, auth.Claims{}, errUnauthorized
	}
	if err != nil {
		return nil, auth.Claims{}, err
	}
	if claims.Version != u.TokenVersion {
		return nil, auth.Claims{}, errUnauthorized
	}
	if !u.IsActive {
		return nil, auth.Claims{}, errInactive
	}
	return u, claims, nil
}

// clientAddr keys the sign-in limiter. RemoteAddr only: X-Forwarded-For is
// spoofable without a configured trusted proxy.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sessionToken reads the Bearer header (API clients) or the session cookie (UI).
func sessionToken(r *http.Request) (token string, fromCookie bool) {
	if t := bearerToken(r); t != "" {
		return t, false
	}
	for _, name := range []string{sessionCookieSecure, sessionCookie} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value, true
		}
	}
	return "", false
}

func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// isTLS reports HTTPS, directly or via X-Forwarded-Proto from a reverse proxy.
func isTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// setSessionCookie sets an HttpOnly, SameSite=Lax cookie (Secure and __Host- under TLS).
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge time.Duration) {
	secure := isTLS(r)
	name := sessionCookie
	if secure {
		name = sessionCookieSecure
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge / time.Second),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookies deletes both cookie names.
func clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{sessionCookie, sessionCookieSecure} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
			Secure: name == sessionCookieSecure, SameSite: http.SameSiteLaxMode,
		})
	}
}

// crossOriginGuard rejects cross-origin unsafe requests (Sec-Fetch-Site/Origin),
// like Go 1.25 http.CrossOriginProtection; also covers the login itself.
func crossOriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !safeMethod(r.Method) && crossOrigin(r) {
			writeError(w, r, http.StatusForbidden, "err.auth.cross_origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func crossOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
		// Older browsers or non-browser clients: check Origin if present.
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		u, err := url.Parse(origin)
		return err != nil || u.Host != r.Host
	default: // same-site, cross-site
		return true
	}
}

// securityHeaders sets CSP and other security headers; imgSrc adds image sources (tile server).
func securityHeaders(next http.Handler, imgSrc string) http.Handler {
	csp := "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:" + imgSrc +
		"; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
