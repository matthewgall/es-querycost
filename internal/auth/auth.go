// Package auth provides request authentication for the query-cost gate.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Context contains user identity information extracted from a request.
type Context struct {
	// UserID is the authenticated user identifier.
	UserID string
	// Plan is the pricing plan to enforce.
	Plan string
	// Extra holds arbitrary claim-derived values.
	Extra map[string]any
}

// ToMap converts the auth context into the map format used by rules.
func (c Context) ToMap() map[string]any {
	m := map[string]any{
		"user": map[string]any{
			"id":   c.UserID,
			"plan": c.Plan,
		},
	}
	for k, v := range c.Extra {
		m[k] = v
	}
	return m
}

// Authenticator validates credentials from an HTTP request.
type Authenticator interface {
	// Authenticate extracts identity from the request. It returns an error when
	// credentials are missing or invalid.
	Authenticate(r *http.Request) (Context, error)
	// Close releases any background resources held by the authenticator.
	Close() error
}

// NoOp allows every request and assigns the default plan.
type NoOp struct{}

// Close is a no-op.
func (n NoOp) Close() error { return nil }

func (NoOp) Authenticate(*http.Request) (Context, error) {
	return Context{Plan: "default"}, nil
}

// APIKey maps hashed API key values to contexts.
type APIKey struct {
	// Keys maps a SHA-256 hash of an API key to the context it represents.
	Keys map[string]Context
}

// NewAPIKey creates an APIKey from a map of plaintext key to context.
func NewAPIKey(raw map[string]Context) APIKey {
	keys := make(map[string]Context, len(raw))
	for k, v := range raw {
		keys[hashKey(k)] = v
	}
	return APIKey{Keys: keys}
}

func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// Close is a no-op.
func (a APIKey) Close() error { return nil }

func (a APIKey) Authenticate(r *http.Request) (Context, error) {
	key, err := extractBearer(r, "apikey")
	if err != nil {
		return Context{}, err
	}
	ctx, ok := a.Keys[hashKey(key)]
	if !ok {
		return Context{}, fmt.Errorf("invalid api key")
	}
	return ctx, nil
}

// JWT validates HMAC-signed JWTs and extracts claims.
type JWT struct {
	// Secret is the HMAC secret used to sign tokens.
	Secret []byte
	// PlanClaim is the JWT claim containing the plan. Defaults to "plan".
	PlanClaim string
	// UserClaim is the JWT claim containing the user ID. Defaults to "sub".
	UserClaim string
}

func (j JWT) Close() error { return nil }

func (j JWT) Authenticate(r *http.Request) (Context, error) {
	token, err := extractBearer(r, "bearer")
	if err != nil {
		return Context{}, err
	}

	planClaim := j.PlanClaim
	if planClaim == "" {
		planClaim = "plan"
	}
	userClaim := j.UserClaim
	if userClaim == "" {
		userClaim = "sub"
	}

	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return j.Secret, nil
	})
	if err != nil || !parsed.Valid {
		return Context{}, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Context{}, fmt.Errorf("invalid token claims")
	}

	ctx := Context{Extra: make(map[string]any)}
	if plan, ok := stringClaim(claims, planClaim); ok {
		ctx.Plan = plan
	}
	if user, ok := stringClaim(claims, userClaim); ok {
		ctx.UserID = user
	}
	for k, v := range claims {
		if k == planClaim || k == userClaim {
			continue
		}
		ctx.Extra[k] = v
	}
	return ctx, nil
}

func extractBearer(r *http.Request, scheme string) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", fmt.Errorf("missing authorization header")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], scheme) {
		return "", fmt.Errorf("missing or invalid authorization scheme")
	}
	return parts[1], nil
}

func stringClaim(claims jwt.MapClaims, key string) (string, bool) {
	raw, ok := claims[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok
}
