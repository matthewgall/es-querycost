package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWKSValid(t *testing.T) {
	keyPair, jwks := newRSAJWKS(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	}))
	defer server.Close()

	j := &JWKS{URL: server.URL}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":  "user-rsa",
		"plan": "enterprise",
	})
	tokenString, err := token.SignedString(keyPair)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, "/search", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	ctx, err := j.Authenticate(req)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if ctx.UserID != "user-rsa" || ctx.Plan != "enterprise" {
		t.Errorf("unexpected context: %+v", ctx)
	}
}

func TestJWKSInvalidSignature(t *testing.T) {
	_, jwks := newRSAJWKS(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(jwks)
	}))
	defer server.Close()

	// Sign with a different key.
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":  "user-rsa",
		"plan": "enterprise",
	})
	tokenString, _ := token.SignedString(otherKey)

	j := &JWKS{URL: server.URL}
	req, _ := http.NewRequest(http.MethodPost, "/search", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	_, err := j.Authenticate(req)
	if err == nil {
		t.Error("expected invalid signature to fail")
	}
}

func TestJWKSIssuerAudience(t *testing.T) {
	keyPair, jwks := newRSAJWKS(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(jwks)
	}))
	defer server.Close()

	j := &JWKS{URL: server.URL, Issuer: "https://idp", Audience: "es-querycost"}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":  "user-rsa",
		"plan": "enterprise",
		"iss":  "https://idp",
		"aud":  "es-querycost",
	})
	tokenString, _ := token.SignedString(keyPair)

	req, _ := http.NewRequest(http.MethodPost, "/search", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	_, err := j.Authenticate(req)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
}

func newRSAJWKS(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())

	jwks := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"use": "sig",
				"kid": "key-1",
				"alg": "RS256",
				"n":   n,
				"e":   e,
			},
		},
	}
	b, _ := json.Marshal(jwks)
	return key, b
}
