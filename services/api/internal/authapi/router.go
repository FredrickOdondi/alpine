package authapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/services/api/internal/apigen"
)

// Router builds the HTTP handler for srv (normally *Server, or a core server embedding it) with
// the auth middleware chain:
//
//	security headers -> CORS -> per-IP rate limit on /v1/auth/* -> authenticate -> CSRF -> routes
func Router(srv apigen.ServerInterface, a *auth.Service) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.SecurityHeaders, a.CORS)
	authLimit := a.RateLimit("auth_endpoints", 60, time.Minute)
	r.Use(func(next http.Handler) http.Handler {
		limited := authLimit(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasPrefix(req.URL.Path, "/v1/auth/") || strings.HasPrefix(req.URL.Path, "/v1/invitations/") {
				limited.ServeHTTP(w, req)
				return
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Use(a.Middleware, a.CSRF)
	return apigen.HandlerWithOptions(srv, apigen.ChiServerOptions{
		BaseRouter: r,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			auth.WriteError(w, http.StatusBadRequest, "invalid_input", err.Error())
		},
	})
}
