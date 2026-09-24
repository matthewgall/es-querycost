package auth

import (
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestNoOp(t *testing.T) {
	a := NoOp{}
	ctx, err := a.Authenticate(&http.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Plan != "default" {
		t.Errorf("expected default plan, got %s", ctx.Plan)
	}
}

func TestAPIKey(t *testing.T) {
	a := NewAPIKey(map[string]Context{
		"secret-key": {UserID: "user-1", Plan: "pro"},
	})

	req, _ := http.NewRequest(http.MethodPost, "/search", nil)
	req.Header.Set("Authorization", "ApiKey secret-key")
	ctx, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.UserID != "user-1" || ctx.Plan != "pro" {
		t.Errorf("unexpected context: %+v", ctx)
	}
}

func TestAPIKeyInvalid(t *testing.T) {
	a := NewAPIKey(map[string]Context{})
	req, _ := http.NewRequest(http.MethodPost, "/search", nil)
	req.Header.Set("Authorization", "ApiKey wrong-key")
	_, err := a.Authenticate(req)
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

func TestJWT(t *testing.T) {
	secret := []byte("test-secret")
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "user-2",
		"plan": "enterprise",
	})
	tokenString, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	a := JWT{Secret: secret}
	req, _ := http.NewRequest(http.MethodPost, "/search", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	ctx, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.UserID != "user-2" || ctx.Plan != "enterprise" {
		t.Errorf("unexpected context: %+v", ctx)
	}
}

func TestJWTInvalid(t *testing.T) {
	a := JWT{Secret: []byte("test-secret")}
	req, _ := http.NewRequest(http.MethodPost, "/search", nil)
	req.Header.Set("Authorization", "Bearer not-a-token")
	_, err := a.Authenticate(req)
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestContextToMap(t *testing.T) {
	ctx := Context{UserID: "u", Plan: "free", Extra: map[string]any{"team": "alpha"}}
	m := ctx.ToMap()
	user := m["user"].(map[string]any)
	if user["id"] != "u" || user["plan"] != "free" {
		t.Errorf("unexpected map: %+v", m)
	}
	if m["team"] != "alpha" {
		t.Errorf("missing extra claim: %+v", m)
	}
}

func TestAuthenticatorClose(t *testing.T) {
	noop := NoOp{}
	if err := noop.Close(); err != nil {
		t.Errorf("NoOp.Close: %v", err)
	}
	ak := APIKey{}
	if err := ak.Close(); err != nil {
		t.Errorf("APIKey.Close: %v", err)
	}
	jwtAuth := JWT{}
	if err := jwtAuth.Close(); err != nil {
		t.Errorf("JWT.Close: %v", err)
	}
}
