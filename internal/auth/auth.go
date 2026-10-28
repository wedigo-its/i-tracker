// Package auth handles password hashing (Argon2id, legacy bcrypt still
// accepted), session tokens (JWT, HS256) with derived CSRF tokens, the
// revocation list of signed-out sessions and the failed-login limiter.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"

	"i-tracker/internal/model"
)

// DefaultSessionTTL is the sliding idle session lifetime if auth.session_ttl is unset.
const DefaultSessionTTL = 12 * time.Hour

// First admin, created while no user exists; the password is random (GeneratePassword).
const (
	DefaultAdminUsername = "admin"
	DefaultAdminEmail    = "admin@iperf-tracker.local"
)

// LegacyDefaultPassword is the fixed default of earlier versions; an admin
// still using it must change it at the next sign-in.
const LegacyDefaultPassword = "admin123"

// MaxPasswordBytes caps password length (guards against huge inputs).
const MaxPasswordBytes = 256

// MinPasswordLength is the minimum length of new passwords in characters;
// only checked when setting, shorter existing passwords stay valid.
const MinPasswordLength = 8

// ErrInvalidToken is returned for malformed, expired or foreign-signed tokens.
var ErrInvalidToken = errors.New("invalid token")

// Argon2id parameters per RFC 9106, section 4 (second recommendation):
// 64 MiB, 3 passes, 4 threads.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns an Argon2id hash in PHC format
// ($argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>).
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword checks password against an Argon2id or (legacy) bcrypt hash.
func CheckPassword(hash, password string) bool {
	if strings.HasPrefix(hash, "$argon2id$") {
		p, err := parseArgon2id(hash)
		if err != nil {
			return false
		}
		key := argon2.IDKey([]byte(password), p.salt, p.time, p.memory, p.threads, uint32(len(p.key)))
		return subtle.ConstantTimeCompare(key, p.key) == 1
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

var (
	dummyOnce sync.Once
	dummyVal  string
)

// DummyHash returns a lazily computed Argon2id hash to check unknown users
// against, so failed sign-ins take equally long (no user oracle via timing).
func DummyHash() string {
	dummyOnce.Do(func() {
		h, err := HashPassword("i-tracker-dummy")
		if err != nil {
			h = "$argon2id$v=19$m=65536,t=3,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}
		dummyVal = h
	})
	return dummyVal
}

// NeedsRehash reports whether hash lacks the current Argon2id parameters
// (e.g. bcrypt) and should be replaced after a successful sign-in.
func NeedsRehash(hash string) bool {
	p, err := parseArgon2id(hash)
	if err != nil {
		return true
	}
	return p.time != argonTime || p.memory != argonMemory || p.threads != argonThreads || len(p.key) != argonKeyLen
}

type argonParams struct {
	time, memory uint32
	threads      uint8
	salt, key    []byte
}

func parseArgon2id(hash string) (argonParams, error) {
	var p argonParams
	parts := strings.Split(hash, "$")
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, errors.New("not an Argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, errors.New("Argon2 version not supported")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return p, err
	}
	var err error
	if p.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, err
	}
	if p.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return p, err
	}
	if p.time == 0 || p.threads == 0 || len(p.key) == 0 {
		return p, errors.New("invalid Argon2id parameters")
	}
	return p, nil
}

// passwordAlphabet omits easily confused characters (0/O, 1/l/I).
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword returns a random 18-character password (about 104 bits).
func GeneratePassword() string {
	const n = 18
	out := make([]byte, n)
	buf := make([]byte, 1)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			panic("random number generator not available: " + err.Error())
		}
		// Rejection sampling instead of plain modulo: keeps characters uniform.
		if int(buf[0]) >= 256-256%len(passwordAlphabet) {
			continue
		}
		out[i] = passwordAlphabet[int(buf[0])%len(passwordAlphabet)]
		i++
	}
	return string(out)
}

// NewAdmin builds an unsaved admin with password; mustChange forces a
// password change at the first sign-in.
func NewAdmin(username, email, password string, mustChange bool) (*model.User, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	return &model.User{
		Username:           username,
		Email:              email,
		HashedPassword:     hash,
		IsActive:           true,
		IsAdmin:            true,
		MustChangePassword: mustChange,
	}, nil
}

// Claims is the content of a session token.
type Claims struct {
	// Username is the signed-in user (sub).
	Username string
	// Version is the user's token version at issue (ver); a password change or
	// "sign out everywhere" bumps it in the DB, invalidating older tokens.
	Version int64
	// SessionID identifies the session (jti); kept on renewal, keys the
	// revocation list and the CSRF token.
	SessionID string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// jwtClaims is the JWT form of Claims.
type jwtClaims struct {
	jwt.RegisteredClaims
	Version int64 `json:"ver"`
}

// Tokens issues and verifies session tokens. Payload:
// {"sub": <username>, "ver": <token_version>, "jti": <session>, "iat": <unix>, "exp": <unix>}.
type Tokens struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewTokens creates a token service with the signing key and sliding session
// lifetime ttl (<= 0: DefaultSessionTTL).
func NewTokens(secret string, ttl time.Duration) *Tokens {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &Tokens{secret: []byte(secret), ttl: ttl, now: time.Now}
}

// TTL returns the session lifetime.
func (t *Tokens) TTL() time.Duration { return t.ttl }

// Create issues a token for a new session of username.
func (t *Tokens) Create(username string, version int64) (string, Claims, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", Claims{}, err
	}
	return t.Renew(Claims{Username: username, Version: version, SessionID: hex.EncodeToString(b)})
}

// Renew issues a full-lifetime token for session c (same SessionID and Version).
func (t *Tokens) Renew(c Claims) (string, Claims, error) {
	now := t.now().Truncate(time.Second)
	c.IssuedAt, c.ExpiresAt = now, now.Add(t.ttl)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   c.Username,
			ID:        c.SessionID,
			IssuedAt:  jwt.NewNumericDate(c.IssuedAt),
			ExpiresAt: jwt.NewNumericDate(c.ExpiresAt),
		},
		Version: c.Version,
	}).SignedString(t.secret)
	return token, c, err
}

// NeedsRenewal reports whether over half of c's lifetime has passed, so the
// server renews the session (sliding expiry).
func (t *Tokens) NeedsRenewal(c Claims) bool {
	return t.now().After(c.IssuedAt.Add(t.ttl / 2))
}

// Parse verifies a token's signature and expiry and returns its claims; the
// caller checks revocation and version.
func (t *Tokens) Parse(token string) (Claims, error) {
	var jc jwtClaims
	_, err := jwt.ParseWithClaims(token, &jc,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(t.now),
	)
	// Tokens without a session ID (earlier versions) are no longer valid.
	if err != nil || jc.Subject == "" || jc.ID == "" || jc.IssuedAt == nil {
		return Claims{}, ErrInvalidToken
	}
	return Claims{
		Username:  jc.Subject,
		Version:   jc.Version,
		SessionID: jc.ID,
		IssuedAt:  jc.IssuedAt.Time,
		ExpiresAt: jc.ExpiresAt.Time,
	}, nil
}

// CSRF derives a session's CSRF token; bound to the session ID (stable across
// renewals), required in the X-CSRF-Token header on mutating requests.
func (t *Tokens) CSRF(sessionID string) string {
	m := hmac.New(sha256.New, t.secret)
	m.Write([]byte("csrf\x00"))
	m.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckCSRF compares a submitted CSRF token in constant time.
func (t *Tokens) CheckCSRF(sessionID, got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(t.CSRF(sessionID)), []byte(got)) == 1
}
