package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer identifies the tokens this service mints, and is checked on the way
// back in so a token from another service cannot be replayed here.
const Issuer = "imageforge"

// signingMethod is the one algorithm accepted, both when signing and when
// verifying.
//
// Pinning it is what closes the algorithm-confusion hole: a verifier that
// accepts whatever the token's own header asks for will happily validate a
// token signed with "none", or verify an RS256 token using the public key as an
// HMAC secret.
var signingMethod = jwt.SigningMethodHS256

// Token lifetime bounds.
const (
	// DefaultTokenTTL is how long an issued token stays valid.
	DefaultTokenTTL = time.Hour
	// clockSkew is the leeway allowed on time-based claims, for the ordinary
	// case of a client whose clock is slightly off.
	clockSkew = 30 * time.Second
)

// Authentication errors. They are wrapped with detail, so compare with
// errors.Is.
var (
	// ErrNoCredentials is returned when a request carries no bearer token.
	ErrNoCredentials = errors.New("no bearer token")
	// ErrInvalidToken is returned when a token is malformed, expired, signed
	// with the wrong key, or otherwise unusable.
	ErrInvalidToken = errors.New("invalid token")
	// ErrNoSigningKey is returned when the issuer has no key to sign with.
	ErrNoSigningKey = errors.New("no signing key configured")
)

// contextKey is unexported so no other package can collide with these keys.
type contextKey struct{ name string }

var clientIDKey = &contextKey{name: "client-id"}

// ClientID returns the authenticated client for a request, and whether the
// request was authenticated at all.
func ClientID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(clientIDKey).(string)
	return id, ok
}

// Claims is the payload of an ImageForge token.
type Claims struct {
	jwt.RegisteredClaims
}

// TokenIssuer mints and verifies tokens.
//
// The zero value is not usable: build one with NewIssuer.
type TokenIssuer struct {
	key    []byte
	ttl    time.Duration
	secret string
	now    func() time.Time
}

// IssuerOption overrides a TokenIssuer setting.
type IssuerOption func(*TokenIssuer)

// WithTokenTTL sets how long an issued token stays valid. A non-positive
// duration leaves the default in place.
func WithTokenTTL(d time.Duration) IssuerOption {
	return func(i *TokenIssuer) {
		if d > 0 {
			i.ttl = d
		}
	}
}

// WithClientSecret requires clients to present this shared secret to be issued
// a token. An empty secret means any client id is accepted, which is only
// appropriate for a local demo.
func WithClientSecret(secret string) IssuerOption {
	return func(i *TokenIssuer) { i.secret = secret }
}

// withClock replaces the clock, for deterministic tests.
func withClock(now func() time.Time) IssuerOption {
	return func(i *TokenIssuer) {
		if now != nil {
			i.now = now
		}
	}
}

// NewIssuer returns an issuer signing with key.
//
// The key must be at least 32 bytes: HS256 keys shorter than the hash output
// weaken the signature, and a short one here would usually mean a passphrase
// someone typed rather than a generated secret.
func NewIssuer(key []byte, opts ...IssuerOption) (*TokenIssuer, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("%w: the signing key must be at least 32 bytes, got %d",
			ErrNoSigningKey, len(key))
	}

	issuer := &TokenIssuer{
		key: append([]byte(nil), key...),
		ttl: DefaultTokenTTL,
		now: time.Now,
	}
	for _, opt := range opts {
		opt(issuer)
	}
	return issuer, nil
}

// GenerateKey returns a random 32-byte signing key.
//
// It is what a server with no configured key falls back to, which means tokens
// do not survive a restart. That is the right trade for a demo: an ephemeral
// key nobody knows beats a default key everybody knows.
func GenerateKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	return key, nil
}

// ParseKey decodes a hex-encoded signing key.
func ParseKey(encoded string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("%w: the key must be hex-encoded: %w", ErrNoSigningKey, err)
	}
	return key, nil
}

// TTL returns how long an issued token stays valid.
func (i *TokenIssuer) TTL() time.Duration { return i.ttl }

// Issue mints a token for clientID.
//
// It is the caller's job to have established that the client is who it says it
// is; Authorize does that for the shared-secret demo flow.
func (i *TokenIssuer) Issue(clientID string) (string, error) {
	if strings.TrimSpace(clientID) == "" {
		return "", errors.New("httpapi: cannot issue a token for an empty client id")
	}

	issuedAt := i.now().UTC()
	token := jwt.NewWithClaims(signingMethod, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Subject:   clientID,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(i.ttl)),
		},
	})

	signed, err := token.SignedString(i.key)
	if err != nil {
		return "", fmt.Errorf("httpapi: sign token: %w", err)
	}
	return signed, nil
}

// Authorize reports whether a client may be issued a token.
//
// With no configured secret every client id is accepted, which is the demo
// default and is why the API logs a warning when it starts that way. The
// comparison is constant-time so a wrong secret cannot be recovered by timing
// the responses.
func (i *TokenIssuer) Authorize(clientID, secret string) bool {
	if strings.TrimSpace(clientID) == "" {
		return false
	}
	if i.secret == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(i.secret)) == 1
}

// Verify checks a signed token and returns the client it belongs to.
func (i *TokenIssuer) Verify(signed string) (string, error) {
	var claims Claims

	_, err := jwt.ParseWithClaims(signed, &claims, func(*jwt.Token) (any, error) {
		return i.key, nil
	},
		// Only the algorithm this service signs with is accepted, whatever the
		// token's own header claims.
		jwt.WithValidMethods([]string{signingMethod.Alg()}),
		jwt.WithIssuer(Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(clockSkew),
		jwt.WithTimeFunc(i.now),
	)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	if strings.TrimSpace(claims.Subject) == "" {
		return "", fmt.Errorf("%w: the token names no subject", ErrInvalidToken)
	}
	return claims.Subject, nil
}

// bearerToken extracts the credentials from an Authorization header.
func bearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", ErrNoCredentials
	}

	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Errorf("%w: the Authorization header must use the Bearer scheme", ErrInvalidToken)
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return "", ErrNoCredentials
	}
	return token, nil
}

// requireAuth rejects any request without a valid token, and puts the client it
// belongs to in the request context.
func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signed, err := bearerToken(r)
		if err == nil {
			var clientID string
			clientID, err = a.issuer.Verify(signed)
			if err == nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIDKey, clientID)))
				return
			}
		}

		// WWW-Authenticate is what tells a client how to authenticate rather
		// than leaving it to guess, and RFC 6750 asks for it on a 401.
		w.Header().Set("WWW-Authenticate", `Bearer realm="imageforge"`)

		// The reason is deliberately coarse: distinguishing "expired" from
		// "bad signature" tells an attacker which half of a forgery worked.
		detail := "This endpoint requires a bearer token. Get one from POST /auth/token."
		if !errors.Is(err, ErrNoCredentials) {
			detail = "The bearer token is missing, malformed or no longer valid."
		}

		a.cfg.Logger.DebugContext(r.Context(), "rejected an unauthenticated request",
			slogPath(r), slogError(err))

		writeProblem(w, r, http.StatusUnauthorized, TypeUnauthorized, "Unauthorized", detail)
	})
}
