package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// JWKS validates RSA/ECDSA/OKP JWTs using keys fetched from a JWKS endpoint.
// It launches a background refresh goroutine so rotated keys are picked up
// automatically.
type JWKS struct {
	// URL is the JWKS endpoint, e.g. https://idp/.well-known/jwks.json
	URL string
	// PlanClaim is the JWT claim containing the plan. Defaults to "plan".
	PlanClaim string
	// UserClaim is the JWT claim containing the user id. Defaults to "sub".
	UserClaim string
	// Issuer, if set, is validated against the "iss" claim.
	Issuer string
	// Audience, if set, is validated against the "aud" claim.
	Audience string
	// RefreshInterval controls how often the JWKS is refreshed. Defaults to 1h.
	RefreshInterval time.Duration

	keyfunc    jwt.Keyfunc
	parsed     bool
	planClaim  string
	userClaim  string
	cancelFunc context.CancelFunc
}

func (j *JWKS) Authenticate(r *http.Request) (Context, error) {
	if err := j.init(); err != nil {
		return Context{}, fmt.Errorf("jwks init: %w", err)
	}

	token, err := extractBearer(r, "bearer")
	if err != nil {
		return Context{}, err
	}

	claims := jwt.MapClaims{}
	opts := []jwt.ParserOption{}
	if j.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(j.Issuer))
	}
	if j.Audience != "" {
		opts = append(opts, jwt.WithAudience(j.Audience))
	}

	parsed, err := jwt.ParseWithClaims(token, claims, j.keyfunc, opts...)
	if err != nil || !parsed.Valid {
		return Context{}, fmt.Errorf("invalid token: %w", err)
	}

	ctx := Context{Extra: make(map[string]any)}
	if plan, ok := stringClaim(claims, j.planClaim); ok {
		ctx.Plan = plan
	}
	if user, ok := stringClaim(claims, j.userClaim); ok {
		ctx.UserID = user
	}
	for k, v := range claims {
		if k == j.planClaim || k == j.userClaim {
			continue
		}
		ctx.Extra[k] = v
	}
	return ctx, nil
}

func (j *JWKS) init() error {
	if j.parsed {
		return nil
	}
	if j.URL == "" {
		return fmt.Errorf("jwks url is required")
	}
	j.planClaim = j.PlanClaim
	if j.planClaim == "" {
		j.planClaim = "plan"
	}
	j.userClaim = j.UserClaim
	if j.userClaim == "" {
		j.userClaim = "sub"
	}

	ctx, cancel := context.WithCancel(context.Background())
	j.cancelFunc = cancel

	kf, err := keyfunc.NewDefaultCtx(ctx, []string{j.URL})
	if err != nil {
		cancel()
		return fmt.Errorf("create jwks keyfunc: %w", err)
	}
	j.keyfunc = kf.Keyfunc
	j.parsed = true
	return nil
}

// Close stops the background JWKS refresh goroutine.
func (j *JWKS) Close() error {
	if j.cancelFunc != nil {
		j.cancelFunc()
	}
	return nil
}
