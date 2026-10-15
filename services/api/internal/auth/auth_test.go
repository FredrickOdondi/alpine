package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	sharedauth "github.com/alpine-dev/alpine/packages/shared/auth"
)

func TestDevStubMiddleware(t *testing.T) {
	org := uuid.New()
	stub := DevStub{UserID: uuid.New(), OrgID: org, Role: sharedauth.RoleOwner, CookieName: "alpine_session"}
	var got *sharedauth.Principal
	h := stub.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = sharedauth.PrincipalFrom(r.Context())
	}))
	cases := []struct {
		name   string
		path   string
		header func(*http.Request)
		want   bool
	}{
		{"anonymous api call", "/v1/tickets", nil, true},
		{"webhook", "/v1/webhooks/github", nil, false},
		{"auth flow", "/v1/auth/dev-login", nil, false},
		{"session cookie", "/v1/tickets", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "alpine_session", Value: "x"}) }, false},
		{"api key", "/v1/tickets", func(r *http.Request) { r.Header.Set("Authorization", "Bearer k") }, false},
	}
	for _, c := range cases {
		got = nil
		r := httptest.NewRequest(http.MethodPost, c.path, nil)
		if c.header != nil {
			c.header(r)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if (got != nil) != c.want {
			t.Errorf("%s: dev principal injected = %v, want %v", c.name, got != nil, c.want)
		}
		if got != nil && (got.CSRFToken != DevStubCSRFToken || got.OrgID != org || got.Kind != sharedauth.KindSession) {
			t.Errorf("%s: unexpected principal %+v", c.name, got)
		}
	}
}
