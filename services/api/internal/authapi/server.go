// Package authapi implements the auth, org, member, invitation, API-key and audit endpoints of
// api/openapi.yaml (apigen.ServerInterface) on top of packages/shared/auth and repo.
//
// Server embeds apigen.Unimplemented, so the core API server can embed *Server and override the
// remaining (ticket/repo/...) operations, or mount Router and add its own routes.
package authapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/repo"
	"github.com/alpine-dev/alpine/services/api/internal/apigen"
)

// Server implements the auth/org slice of apigen.ServerInterface.
type Server struct {
	apigen.Unimplemented
	Auth  *auth.Service
	Store repo.Store
}

var _ apigen.ServerInterface = (*Server)(nil)

// New returns a Server.
func New(a *auth.Service) *Server { return &Server{Auth: a, Store: a.Store} }

// --- helpers -----------------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_input", "invalid JSON body")
		return false
	}
	return true
}

func principal(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p := auth.PrincipalFrom(r.Context())
	if p == nil {
		auth.WriteErr(w, auth.ErrUnauthenticated)
		return nil, false
	}
	return p, true
}

func sessionPrincipal(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, ok := principal(w, r)
	if ok && p.Kind != auth.KindSession {
		auth.WriteError(w, http.StatusForbidden, "session_required", "this endpoint requires a browser session")
		return nil, false
	}
	return p, ok
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toUser(u repo.User) apigen.User {
	v := u.EmailVerified
	return apigen.User{Id: u.ID, Email: strp(u.Email), EmailVerified: &v, Name: strp(u.Name), AvatarUrl: strp(u.AvatarURL), GithubLogin: strp(u.GitHubLogin)}
}

func toOrg(o repo.Org) apigen.Org {
	return apigen.Org{Id: o.ID, Slug: o.Slug, Name: o.Name, Plan: apigen.OrgPlan(o.Plan)}
}

func (s *Server) toInvitation(i repo.Invitation) apigen.Invitation {
	return apigen.Invitation{
		Id: i.ID, OrgId: i.OrgID, Email: openapi_types.Email(i.Email), Role: apigen.Role(i.Role),
		Status: apigen.InvitationStatus(i.Status(time.Now())), InvitedBy: i.InvitedBy, ExpiresAt: i.ExpiresAt,
		AcceptedAt: i.AcceptedAt, CreatedAt: i.CreatedAt,
	}
}

func toAPIKey(k repo.APIKey) apigen.ApiKey {
	scopes := make([]apigen.ApiKeyScope, len(k.Scopes))
	for i, s := range k.Scopes {
		scopes[i] = apigen.ApiKeyScope(s)
	}
	return apigen.ApiKey{
		Id: k.ID, OrgId: k.OrgID, Name: k.Name, Prefix: k.Prefix, Scopes: scopes, Role: apigen.ApiKeyRole(k.Role),
		CreatedBy: k.CreatedBy, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt, CreatedAt: k.CreatedAt,
	}
}

// --- auth --------------------------------------------------------------------------------------

// GetAuthProviders implements apigen.ServerInterface.
func (s *Server) GetAuthProviders(w http.ResponseWriter, r *http.Request) {
	g, gh, ml, dev := s.Auth.EnabledProviders()
	writeJSON(w, http.StatusOK, apigen.AuthProviders{Google: g, Github: gh, MagicLink: ml, DevLogin: dev})
}

func (s *Server) startLogin(w http.ResponseWriter, r *http.Request, provider string, redirectTo *string, link *bool) {
	rt := ""
	if redirectTo != nil {
		rt = *redirectTo
	}
	u, err := s.Auth.StartLogin(w, r, provider, rt, link != nil && *link)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, provider string) {
	u, err := s.Auth.FinishLogin(w, r, provider)
	if err != nil {
		http.Redirect(w, r, s.Auth.LoginErrorURL(err), http.StatusFound)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// StartGoogleLogin implements apigen.ServerInterface.
func (s *Server) StartGoogleLogin(w http.ResponseWriter, r *http.Request, params apigen.StartGoogleLoginParams) {
	s.startLogin(w, r, "google", params.RedirectTo, params.Link)
}

// GoogleCallback implements apigen.ServerInterface.
func (s *Server) GoogleCallback(w http.ResponseWriter, r *http.Request, _ apigen.GoogleCallbackParams) {
	s.finishLogin(w, r, "google")
}

// StartGithubLogin implements apigen.ServerInterface.
func (s *Server) StartGithubLogin(w http.ResponseWriter, r *http.Request, params apigen.StartGithubLoginParams) {
	s.startLogin(w, r, "github", params.RedirectTo, params.Link)
}

// GithubCallback implements apigen.ServerInterface.
func (s *Server) GithubCallback(w http.ResponseWriter, r *http.Request, _ apigen.GithubCallbackParams) {
	s.finishLogin(w, r, "github")
}

// DevLogin implements apigen.ServerInterface.
func (s *Server) DevLogin(w http.ResponseWriter, r *http.Request, _ apigen.DevLoginParams) {
	if err := s.Auth.DevLogin(w, r); err != nil {
		if errors.Is(err, auth.ErrNotConfigured) || errors.Is(err, repo.ErrNotFound) {
			auth.WriteError(w, http.StatusNotFound, "not_found", "dev login disabled or dev user not seeded")
			return
		}
		auth.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RequestMagicLink implements apigen.ServerInterface.
func (s *Server) RequestMagicLink(w http.ResponseWriter, r *http.Request) {
	var body apigen.MagicLinkRequest
	if !decode(w, r, &body) {
		return
	}
	rt := ""
	if body.RedirectTo != nil {
		rt = *body.RedirectTo
	}
	if err := s.Auth.RequestMagicLink(r, string(body.Email), rt); err != nil {
		auth.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// VerifyMagicLink implements apigen.ServerInterface.
func (s *Server) VerifyMagicLink(w http.ResponseWriter, r *http.Request) {
	var body apigen.TokenRequest
	if !decode(w, r, &body) {
		return
	}
	rt, err := s.Auth.VerifyMagicLink(w, r, body.Token)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apigen.LoginResult{RedirectTo: rt})
}

// GetCsrfToken implements apigen.ServerInterface.
func (s *Server) GetCsrfToken(w http.ResponseWriter, r *http.Request) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, apigen.CsrfToken{CsrfToken: p.CSRFToken})
}

// Logout implements apigen.ServerInterface.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request, _ apigen.LogoutParams) {
	if err := s.Auth.Logout(w, r); err != nil {
		auth.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LogoutAll implements apigen.ServerInterface.
func (s *Server) LogoutAll(w http.ResponseWriter, r *http.Request, _ apigen.LogoutAllParams) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	n, err := s.Auth.LogoutEverywhere(w, r, p)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apigen.LogoutAllResult{RevokedSessions: n})
}

// GetMe implements apigen.ServerInterface.
func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	u, err := s.Store.GetUser(ctx, p.UserID)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	orgs, err := s.Store.ListOrgsForUser(ctx, p.UserID)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	me := apigen.Me{User: toUser(u), CsrfToken: p.CSRFToken, AuthMethod: apigen.MeAuthMethod(p.AuthMethod), Memberships: []apigen.OrgMembership{}}
	for _, m := range orgs {
		om := apigen.OrgMembership{Org: toOrg(m.Org), Role: apigen.Role(m.Role)}
		me.Memberships = append(me.Memberships, om)
		if m.Org.ID == p.OrgID {
			org, role := om.Org, om.Role
			me.ActiveOrg, me.Role = &org, &role
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, me)
}

// ListMyIdentities implements apigen.ServerInterface.
func (s *Server) ListMyIdentities(w http.ResponseWriter, r *http.Request) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	ids, err := s.Store.ListIdentities(r.Context(), p.UserID)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	items := []apigen.Identity{}
	for _, i := range ids {
		v := i.EmailVerified
		items = append(items, apigen.Identity{Provider: apigen.IdentityProvider(i.Provider), Email: strp(i.Email), EmailVerified: &v,
			Login: strp(i.Login), HostedDomain: strp(i.HostedDomain), CreatedAt: i.CreatedAt, LastLoginAt: i.LastLoginAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// SetActiveOrg implements apigen.ServerInterface.
func (s *Server) SetActiveOrg(w http.ResponseWriter, r *http.Request, _ apigen.SetActiveOrgParams) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	var body apigen.SetActiveOrgRequest
	if !decode(w, r, &body) {
		return
	}
	if err := s.Auth.SetActiveOrg(r, p, body.OrgId); err != nil {
		auth.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- orgs --------------------------------------------------------------------------------------

// ListOrgs implements apigen.ServerInterface.
func (s *Server) ListOrgs(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	items := []apigen.OrgMembership{}
	if p.Kind == auth.KindAPIKey {
		o, err := s.Store.GetOrg(r.Context(), p.OrgID)
		if err != nil {
			auth.WriteErr(w, err)
			return
		}
		items = append(items, apigen.OrgMembership{Org: toOrg(o), Role: apigen.Role(p.Role)})
	} else {
		orgs, err := s.Store.ListOrgsForUser(r.Context(), p.UserID)
		if err != nil {
			auth.WriteErr(w, err)
			return
		}
		for _, m := range orgs {
			items = append(items, apigen.OrgMembership{Org: toOrg(m.Org), Role: apigen.Role(m.Role)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// CreateOrg implements apigen.ServerInterface.
func (s *Server) CreateOrg(w http.ResponseWriter, r *http.Request, _ apigen.CreateOrgParams) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	var body apigen.CreateOrgRequest
	if !decode(w, r, &body) {
		return
	}
	slug := ""
	if body.Slug != nil {
		slug = *body.Slug
	}
	o, err := s.Auth.CreateOrg(r, p, body.Name, slug)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	if p.OrgID == uuid.Nil {
		_ = s.Auth.SetActiveOrg(r, p, o.ID)
	}
	writeJSON(w, http.StatusCreated, toOrg(o))
}

// GetOrg implements apigen.ServerInterface.
func (s *Server) GetOrg(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if _, err := s.Auth.Authorize(r.Context(), p, orgID, auth.PermOrgRead); err != nil {
		// Do not reveal whether the org exists.
		auth.WriteErr(w, repo.ErrNotFound)
		return
	}
	o, err := s.Store.GetOrg(r.Context(), orgID)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrg(o))
}

// UpdateOrg implements apigen.ServerInterface.
func (s *Server) UpdateOrg(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, _ apigen.UpdateOrgParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body apigen.UpdateOrgRequest
	if !decode(w, r, &body) {
		return
	}
	sp, err := s.Auth.Authorize(r.Context(), p, orgID, auth.PermOrgUpdate)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 200 {
		auth.WriteErr(w, fmt.Errorf("%w: name", auth.ErrInvalidInput))
		return
	}
	o, err := s.Store.UpdateOrg(r.Context(), s.Auth.Actor(r, sp), orgID, name)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrg(o))
}

// DeleteOrg implements apigen.ServerInterface.
func (s *Server) DeleteOrg(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, _ apigen.DeleteOrgParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := s.Auth.DeleteOrg(r, p, orgID); err != nil {
		auth.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- members -----------------------------------------------------------------------------------

func toMember(m repo.Member) apigen.Member {
	u := repo.User{ID: m.UserID, Email: m.Email, Name: m.Name, AvatarURL: m.AvatarURL, GitHubLogin: m.GitHubLogin}
	au := toUser(u)
	au.EmailVerified = nil
	return apigen.Member{User: au, Role: apigen.Role(m.Role), JoinedAt: m.CreatedAt, LastLoginAt: m.LastLoginAt}
}

// ListMembers implements apigen.ServerInterface.
func (s *Server) ListMembers(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if _, err := s.Auth.Authorize(r.Context(), p, orgID, auth.PermMembersRead); err != nil {
		auth.WriteErr(w, err)
		return
	}
	ms, err := s.Store.ListMembers(r.Context(), orgID)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	items := make([]apigen.Member, 0, len(ms))
	for _, m := range ms {
		items = append(items, toMember(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// UpdateMember implements apigen.ServerInterface.
func (s *Server) UpdateMember(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, userID apigen.UserId, _ apigen.UpdateMemberParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body apigen.UpdateMemberRequest
	if !decode(w, r, &body) {
		return
	}
	if _, err := s.Auth.UpdateMemberRole(r, p, orgID, userID, auth.Role(body.Role)); err != nil {
		auth.WriteErr(w, err)
		return
	}
	ms, err := s.Store.ListMembers(r.Context(), orgID)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	for _, m := range ms {
		if m.UserID == userID {
			writeJSON(w, http.StatusOK, toMember(m))
			return
		}
	}
	auth.WriteErr(w, repo.ErrNotFound)
}

// RemoveMember implements apigen.ServerInterface.
func (s *Server) RemoveMember(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, userID apigen.UserId, _ apigen.RemoveMemberParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := s.Auth.RemoveMember(r, p, orgID, userID); err != nil {
		auth.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- invitations -------------------------------------------------------------------------------

// ListInvitations implements apigen.ServerInterface.
func (s *Server) ListInvitations(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, params apigen.ListInvitationsParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if _, err := s.Auth.Authorize(r.Context(), p, orgID, auth.PermMembersInvite); err != nil {
		auth.WriteErr(w, err)
		return
	}
	pending := params.Status == nil || *params.Status == apigen.ListInvitationsParamsStatusPending
	invs, err := s.Store.ListInvitations(r.Context(), orgID, pending)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	items := make([]apigen.Invitation, 0, len(invs))
	for _, i := range invs {
		items = append(items, s.toInvitation(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// CreateInvitation implements apigen.ServerInterface.
func (s *Server) CreateInvitation(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, _ apigen.CreateInvitationParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body apigen.CreateInvitationRequest
	if !decode(w, r, &body) {
		return
	}
	inv, err := s.Auth.Invite(r, p, orgID, string(body.Email), auth.Role(body.Role))
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toInvitation(inv))
}

// RevokeInvitation implements apigen.ServerInterface.
func (s *Server) RevokeInvitation(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, invitationID apigen.InvitationId, _ apigen.RevokeInvitationParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := s.Auth.RevokeInvitation(r, p, orgID, invitationID); err != nil {
		auth.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AcceptInvitation implements apigen.ServerInterface.
func (s *Server) AcceptInvitation(w http.ResponseWriter, r *http.Request, _ apigen.AcceptInvitationParams) {
	p, ok := sessionPrincipal(w, r)
	if !ok {
		return
	}
	var body apigen.TokenRequest
	if !decode(w, r, &body) {
		return
	}
	inv, m, err := s.Auth.AcceptInvitation(r, p, body.Token)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	o, err := s.Store.GetOrg(r.Context(), inv.OrgID)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apigen.OrgMembership{Org: toOrg(o), Role: apigen.Role(m.Role)})
}

// --- API keys ----------------------------------------------------------------------------------

// ListApiKeys implements apigen.ServerInterface.
func (s *Server) ListApiKeys(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if _, err := s.Auth.Authorize(r.Context(), p, orgID, auth.PermAPIKeysRead); err != nil {
		auth.WriteErr(w, err)
		return
	}
	keys, err := s.Store.ListAPIKeys(r.Context(), orgID)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	items := make([]apigen.ApiKey, 0, len(keys))
	for _, k := range keys {
		items = append(items, toAPIKey(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// CreateApiKey implements apigen.ServerInterface.
func (s *Server) CreateApiKey(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, _ apigen.CreateApiKeyParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body apigen.CreateApiKeyRequest
	if !decode(w, r, &body) {
		return
	}
	in := auth.NewAPIKeyInput{Name: body.Name, ExpiresAt: body.ExpiresAt}
	if body.Role != nil {
		in.Role = auth.Role(*body.Role)
	}
	for _, sc := range body.Scopes {
		in.Scopes = append(in.Scopes, string(sc))
	}
	k, token, err := s.Auth.CreateAPIKey(r, p, orgID, in)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, apigen.ApiKeyCreated{ApiKey: toAPIKey(k), Token: token})
}

// RevokeApiKey implements apigen.ServerInterface.
func (s *Server) RevokeApiKey(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, keyID apigen.KeyId, _ apigen.RevokeApiKeyParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := s.Auth.RevokeAPIKey(r, p, orgID, keyID); err != nil {
		auth.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- audit -------------------------------------------------------------------------------------

func encodeCursor(at time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixNano(), 10) + ":" + strconv.FormatInt(id, 10)))
}

func decodeCursor(c string) (*time.Time, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, 0, err
	}
	ns, id, ok := strings.Cut(string(b), ":")
	if !ok {
		return nil, 0, errors.New("bad cursor")
	}
	n, err1 := strconv.ParseInt(ns, 10, 64)
	i, err2 := strconv.ParseInt(id, 10, 64)
	if err1 != nil || err2 != nil {
		return nil, 0, errors.New("bad cursor")
	}
	t := time.Unix(0, n)
	return &t, i, nil
}

// ListAuditLog implements apigen.ServerInterface.
func (s *Server) ListAuditLog(w http.ResponseWriter, r *http.Request, orgID apigen.OrgId, params apigen.ListAuditLogParams) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if _, err := s.Auth.Authorize(r.Context(), p, orgID, auth.PermAuditRead); err != nil {
		auth.WriteErr(w, err)
		return
	}
	f := repo.AuditFilter{Limit: 50}
	if params.Limit != nil {
		f.Limit = *params.Limit
	}
	if params.Action != nil {
		f.Action = *params.Action
	}
	if params.Cursor != nil {
		at, id, err := decodeCursor(*params.Cursor)
		if err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_input", "invalid cursor")
			return
		}
		f.BeforeAt, f.BeforeID = at, id
	}
	entries, err := s.Store.List(r.Context(), orgID, f)
	if err != nil {
		auth.WriteErr(w, err)
		return
	}
	page := apigen.AuditLogPage{Items: make([]apigen.AuditEntry, 0, len(entries))}
	for _, e := range entries {
		md := e.Metadata
		if md == nil {
			md = map[string]any{}
		}
		page.Items = append(page.Items, apigen.AuditEntry{Id: e.ID, ActorType: apigen.AuditEntryActorType(e.Actor.Type), ActorId: e.Actor.ID,
			Action: e.Action, TargetType: e.TargetType, TargetId: e.TargetID, Metadata: md, At: e.At})
	}
	if n := len(entries); n > 0 && n == f.Limit {
		c := encodeCursor(entries[n-1].At, entries[n-1].ID)
		page.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, page)
}
