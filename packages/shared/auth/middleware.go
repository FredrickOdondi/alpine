package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// APIError is the JSON error body (matches the OpenAPI Error schema).
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError writes a JSON error.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{Code: code, Message: msg})
}

// StatusFor maps auth/repo errors to HTTP status codes.
func StatusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrCSRF), errors.Is(err, ErrDomainNotAllowed),
		errors.Is(err, ErrSignupDisabled), errors.Is(err, repo.ErrInvitationMismatch), errors.Is(err, repo.ErrUserDisabled):
		return http.StatusForbidden
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrLocked):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrNotConfigured):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrNoActiveOrg), errors.Is(err, ErrInvalidInput), errors.Is(err, ErrInvalidState),
		errors.Is(err, ErrEmailUnverified):
		return http.StatusBadRequest
	case errors.Is(err, repo.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, repo.ErrConflict), errors.Is(err, repo.ErrLastOwner), errors.Is(err, repo.ErrIdentityTaken):
		return http.StatusConflict
	case errors.Is(err, repo.ErrInvitationExpired), errors.Is(err, repo.ErrInvitationInvalid):
		return http.StatusGone
	}
	return http.StatusInternalServerError
}

// WriteErr writes err with its mapped status and stable code (internal errors are not echoed).
func WriteErr(w http.ResponseWriter, err error) {
	status := StatusFor(err)
	msg := err.Error()
	if status == http.StatusInternalServerError {
		msg = "internal error"
	}
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}
	WriteError(w, status, ErrorCode(err), msg)
}

// Middleware authenticates the request when credentials are present and stores the Principal in
// the context. Requests without credentials pass through anonymously (use RequireAuth). Invalid
// credentials are rejected with 401 so a client never silently runs as anonymous.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasCreds := r.Header.Get("Authorization") != "" || s.rawSession(r) != ""
		if !hasCreds {
			next.ServeHTTP(w, r)
			return
		}
		p, err := s.AuthenticatePrincipal(r)
		if err != nil {
			if errors.Is(err, ErrUnauthenticated) && r.Header.Get("Authorization") == "" {
				// Stale/expired cookie: clear it and continue anonymously (login endpoints must work).
				s.clearSessionCookie(w)
				next.ServeHTTP(w, r)
				return
			}
			WriteErr(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// RequireAuth rejects anonymous requests.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if PrincipalFrom(r.Context()) == nil {
			WriteErr(w, ErrUnauthenticated)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePermission requires the principal to hold perm in its ACTIVE org (session) or key org.
// Routes addressing an org by path should call Service.Authorize with that org instead.
func RequirePermission(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := PrincipalFrom(r.Context())
			switch {
			case p == nil:
				WriteErr(w, ErrUnauthenticated)
			case p.OrgID == uuid.Nil:
				WriteErr(w, ErrNoActiveOrg)
			case !p.Can(perm):
				WriteErr(w, ErrForbidden)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

func (s *Service) originAllowed(origin string) bool {
	if slices.Contains(s.Cfg.AllowedOrigins, origin) {
		return true
	}
	// Same-origin requests to the API itself (e.g. Swagger UI served by the API).
	if u, err := url.Parse(s.Cfg.PublicAPIURL); err == nil && origin == u.Scheme+"://"+u.Host {
		return true
	}
	return false
}

// CSRFHeader carries the per-session CSRF token on state-changing requests.
const CSRFHeader = "X-CSRF-Token"

// CSRF protects cookie-authenticated, state-changing requests from the SPA:
//  1. the Origin (or Referer) header, when present, must be an allowed origin;
//  2. session-authenticated requests must echo the session's CSRF token in X-CSRF-Token
//     (synchronizer token; the SPA reads it from GET /v1/auth/csrf or /v1/me). The custom header
//     also forces a CORS preflight, which only allowed origins pass.
//
// API-key requests (Authorization header, no cookies) are exempt. Run after Middleware.
func (s *Service) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !unsafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		p := PrincipalFrom(r.Context())
		if p != nil && p.Kind == KindAPIKey {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Host != "" {
				origin = ref.Scheme + "://" + ref.Host
			}
		}
		if origin != "" && !s.originAllowed(origin) {
			WriteErr(w, ErrCSRF)
			return
		}
		if p != nil && p.Kind == KindSession {
			if tok := r.Header.Get(CSRFHeader); tok == "" || !ConstantTimeEqual(tok, p.CSRFToken) {
				WriteErr(w, ErrCSRF)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// CORS allows credentialed requests from AllowedOrigins only.
func (s *Service) CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(s.Cfg.AllowedOrigins, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", strings.Join([]string{"Content-Type", "Authorization", CSRFHeader, "X-Request-Id"}, ", "))
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimit limits requests per client IP for a bucket (e.g. all /v1/auth/* endpoints).
func (s *Service) RateLimit(bucket string, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retry, err := s.Limiter.Allow(r.Context(), bucket, s.ClientIP(r).String(), limit, window)
			if err != nil {
				// Fail open on Redis errors for availability; lockouts still apply where they matter.
				s.Logger.Warn("auth: rate limiter unavailable", "bucket", bucket, "err", err)
				next.ServeHTTP(w, r)
				return
			}
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets conservative headers on API responses.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") || r.URL.Path == "/v1/me" {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
