//go:build integration

package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"es-querycost/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

func TestProxyJWKSAuth(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	jwksHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(rsaJWKS(&priv.PublicKey, "test")))
	})
	jwksSrv := httptest.NewServer(jwksHandler)
	defer jwksSrv.Close()

	es := httptest.NewServer(esSearchHandler())
	defer es.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = es.URL
	cfg.Auth.Type = "jwks"
	cfg.Auth.JWKSURL = jwksSrv.URL
	cfg.Auth.JWTIssuer = "test-issuer"
	cfg.Auth.JWTAudience = "test-audience"

	handler := newTestServer(t, cfg)

	validToken, err := signRS256Token(priv, "test", jwt.MapClaims{
		"sub":  "user-jwks",
		"plan": "free",
		"iss":  "test-issuer",
		"aud":  "test-audience",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	body := []byte(`{"query":"asn:AS13335","index":"i","context":{"user":{"plan":"free"}}}`)

	t.Run("valid token", func(t *testing.T) {
		w := postSearch(t, handler, body, map[string]string{"Authorization": "Bearer " + validToken})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("missing token", func(t *testing.T) {
		w := postSearch(t, handler, body, nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		w := postSearch(t, handler, body, map[string]string{"Authorization": "Bearer not-a-token"})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("wrong signature", func(t *testing.T) {
		otherPriv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate other key: %v", err)
		}
		wrongToken, err := signRS256Token(otherPriv, "test", jwt.MapClaims{
			"sub":  "user-jwks",
			"plan": "free",
			"iss":  "test-issuer",
			"aud":  "test-audience",
			"exp":  time.Now().Add(time.Hour).Unix(),
		})
		if err != nil {
			t.Fatalf("sign wrong token: %v", err)
		}
		w := postSearch(t, handler, body, map[string]string{"Authorization": "Bearer " + wrongToken})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for wrong signature, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("expired token", func(t *testing.T) {
		expiredToken, err := signRS256Token(priv, "test", jwt.MapClaims{
			"sub":  "user-jwks",
			"plan": "free",
			"iss":  "test-issuer",
			"aud":  "test-audience",
			"exp":  time.Now().Add(-time.Hour).Unix(),
		})
		if err != nil {
			t.Fatalf("sign expired token: %v", err)
		}
		w := postSearch(t, handler, body, map[string]string{"Authorization": "Bearer " + expiredToken})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for expired token, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("issuer mismatch", func(t *testing.T) {
		badToken, err := signRS256Token(priv, "test", jwt.MapClaims{
			"sub":  "user-jwks",
			"plan": "free",
			"iss":  "other-issuer",
			"aud":  "test-audience",
			"exp":  time.Now().Add(time.Hour).Unix(),
		})
		if err != nil {
			t.Fatalf("sign bad issuer token: %v", err)
		}
		w := postSearch(t, handler, body, map[string]string{"Authorization": "Bearer " + badToken})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for issuer mismatch, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func rsaJWKS(pub *rsa.PublicKey, kid string) string {
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	j := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": kid,
				"n":   n,
				"e":   e,
			},
		},
	}
	b, _ := json.Marshal(j)
	return string(b)
}

func signRS256Token(priv *rsa.PrivateKey, kid string, claims jwt.MapClaims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return signed, nil
}
