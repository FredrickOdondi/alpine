package auth

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// Org, membership, invitation and API-key operations with their authorization rules. Each method
// takes the authenticated principal; the repository writes the audit entry atomically.

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// Slugify derives a URL slug from a name.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	if len(s) < 2 {
		s = "org"
	}
	return s
}

// CreateOrg creates an org with the caller as owner. An empty slug is derived from the name with
// a random suffix on collision.
func (s *Service) CreateOrg(r *http.Request, p *Principal, name, slug string) (repo.Org, error) {
	if p == nil || p.Kind != KindSession {
		return repo.Org{}, ErrUnauthenticated
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return repo.Org{}, fmt.Errorf("%w: name", ErrInvalidInput)
	}
	explicit := slug != ""
	if !explicit {
		slug = Slugify(name)
	}
	if !slugRE.MatchString(slug) {
		return repo.Org{}, fmt.Errorf("%w: slug", ErrInvalidInput)
	}
	for attempt := 0; ; attempt++ {
		o, err := s.Store.CreateOrg(r.Context(), s.Actor(r, p), p.UserID, slug, name)
		if errors.Is(err, repo.ErrConflict) && !explicit && attempt < 3 {
			slug = Slugify(name) + "-" + strings.ToLower(RandomToken(4))[:5]
			slug = strings.NewReplacer("_", "x").Replace(slug)
			continue
		}
		return o, err
	}
}

// UpdateMemberRole changes a member's role. Owners can assign any role; admins can manage
// non-owners; nobody can modify a member who outranks them. The DB keeps >= 1 owner.
func (s *Service) UpdateMemberRole(r *http.Request, p *Principal, orgID, userID uuid.UUID, role Role) (repo.Membership, error) {
	ctx := r.Context()
	sp, err := s.Authorize(ctx, p, orgID, PermMembersUpdate)
	if err != nil {
		return repo.Membership{}, err
	}
	if !role.Valid() {
		return repo.Membership{}, fmt.Errorf("%w: role", ErrInvalidInput)
	}
	target, err := s.Store.GetMembership(ctx, orgID, userID)
	if err != nil {
		return repo.Membership{}, err
	}
	if !CanAssignRole(sp.Role, role) || !CanAssignRole(sp.Role, Role(target.Role)) {
		return repo.Membership{}, ErrForbidden
	}
	return s.Store.UpdateMemberRole(ctx, s.Actor(r, sp), orgID, userID, repo.Role(role))
}

// RemoveMember removes a member. Anyone may leave an org themselves (unless last owner);
// removing others needs members:remove and must not target a higher role.
func (s *Service) RemoveMember(r *http.Request, p *Principal, orgID, userID uuid.UUID) error {
	ctx := r.Context()
	sp, err := s.OrgAccess(ctx, p, orgID)
	if err != nil {
		return err
	}
	if sp.Kind != KindSession || sp.UserID != userID {
		if !sp.Can(PermMembersRemove) {
			return ErrForbidden
		}
		target, err := s.Store.GetMembership(ctx, orgID, userID)
		if err != nil {
			return err
		}
		if !CanAssignRole(sp.Role, Role(target.Role)) {
			return ErrForbidden
		}
	}
	return s.Store.RemoveMember(ctx, s.Actor(r, sp), orgID, userID)
}

// Invite creates (or re-issues) an invitation and emails the accept link.
func (s *Service) Invite(r *http.Request, p *Principal, orgID uuid.UUID, email string, role Role) (repo.Invitation, error) {
	ctx := r.Context()
	sp, err := s.Authorize(ctx, p, orgID, PermMembersInvite)
	if err != nil {
		return repo.Invitation{}, err
	}
	if !role.Valid() || !CanAssignRole(sp.Role, role) {
		return repo.Invitation{}, fmt.Errorf("%w: role", ErrForbidden)
	}
	email, err = NormalizeEmail(email)
	if err != nil {
		return repo.Invitation{}, err
	}
	if ok, _, err := s.Limiter.Allow(ctx, "invite_org", orgID.String(), 100, 24*time.Hour); err != nil {
		return repo.Invitation{}, err
	} else if !ok {
		return repo.Invitation{}, ErrRateLimited
	}
	org, err := s.Store.GetOrg(ctx, orgID)
	if err != nil {
		return repo.Invitation{}, err
	}
	token := RandomToken(32)
	inv, err := s.Store.CreateInvitation(ctx, s.Actor(r, sp), orgID, email, repo.Role(role), HashToken(token), s.now().Add(s.Cfg.InvitationTTL))
	if err != nil {
		return repo.Invitation{}, err
	}
	link := s.Cfg.AppURL + "/invitations/accept#token=" + token
	if err := s.Mailer.Send(ctx, Message{
		To: email, Subject: "You're invited to " + org.Name + " on Alpine", Link: link,
		Text: "You've been invited to join " + org.Name + " on Alpine as " + string(role) + ".\n\nAccept: " + link +
			"\n\nThis invitation expires on " + inv.ExpiresAt.UTC().Format(time.RFC1123) + ".",
	}); err != nil {
		s.Logger.Warn("auth: invitation email failed", "org", orgID, "err", err)
	}
	return inv, nil
}

// RevokeInvitation revokes a pending invitation.
func (s *Service) RevokeInvitation(r *http.Request, p *Principal, orgID, invitationID uuid.UUID) error {
	sp, err := s.Authorize(r.Context(), p, orgID, PermMembersInvite)
	if err != nil {
		return err
	}
	return s.Store.RevokeInvitation(r.Context(), s.Actor(r, sp), orgID, invitationID)
}

// AcceptInvitation joins the caller to the inviting org. The caller's VERIFIED email must match
// the invitation. Failures count toward a per-user lockout (token guessing).
func (s *Service) AcceptInvitation(r *http.Request, p *Principal, token string) (repo.Invitation, repo.Membership, error) {
	ctx := r.Context()
	if p == nil || p.Kind != KindSession {
		return repo.Invitation{}, repo.Membership{}, ErrUnauthenticated
	}
	subject := p.UserID.String()
	if d, err := s.Lockout.Check(ctx, "invite_accept", subject); err != nil {
		return repo.Invitation{}, repo.Membership{}, err
	} else if d > 0 {
		return repo.Invitation{}, repo.Membership{}, ErrLocked
	}
	user, err := s.Store.GetUser(ctx, p.UserID)
	if err != nil {
		return repo.Invitation{}, repo.Membership{}, err
	}
	actor := repo.Actor{Type: "user", ID: user.ID.String(), IP: s.ipPtr(r), RequestID: r.Header.Get("X-Request-Id")}
	inv, m, err := s.Store.AcceptInvitation(ctx, actor, HashToken(token), user)
	if err != nil {
		if errors.Is(err, repo.ErrInvitationInvalid) || errors.Is(err, repo.ErrInvitationMismatch) {
			_, _ = s.Lockout.Fail(ctx, "invite_accept", subject)
		}
		return repo.Invitation{}, repo.Membership{}, err
	}
	_ = s.Lockout.Reset(ctx, "invite_accept", subject)
	s.authEvent(ctx, r, &user.ID, &inv.OrgID, "invitation.accepted", "", true, "", nil)
	// Make the newly joined org active if the session has none.
	if p.OrgID == uuid.Nil {
		_ = s.SetActiveOrg(r, p, inv.OrgID)
	}
	return inv, m, nil
}

// NewAPIKeyInput describes a key to create.
type NewAPIKeyInput struct {
	Name      string
	Scopes    []string
	Role      Role
	ExpiresAt *time.Time
}

// CreateAPIKey creates a key and returns it with the plaintext token (shown exactly once). A key
// can never carry more privilege than its creator.
func (s *Service) CreateAPIKey(r *http.Request, p *Principal, orgID uuid.UUID, in NewAPIKeyInput) (repo.APIKey, string, error) {
	ctx := r.Context()
	sp, err := s.Authorize(ctx, p, orgID, PermAPIKeysManage)
	if err != nil {
		return repo.APIKey{}, "", err
	}
	if sp.Kind != KindSession {
		return repo.APIKey{}, "", ErrForbidden // API keys cannot mint API keys
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return repo.APIKey{}, "", fmt.Errorf("%w: name", ErrInvalidInput)
	}
	if in.Role == "" {
		in.Role = RoleMember
	}
	if !slices.Contains(APIKeyRoles, in.Role) || in.Role.Rank() > sp.Role.Rank() {
		return repo.APIKey{}, "", fmt.Errorf("%w: role", ErrInvalidInput)
	}
	if len(in.Scopes) == 0 {
		return repo.APIKey{}, "", fmt.Errorf("%w: scopes", ErrInvalidInput)
	}
	slices.Sort(in.Scopes)
	in.Scopes = slices.Compact(in.Scopes)
	for _, sc := range in.Scopes {
		if !ValidScope(sc) {
			return repo.APIKey{}, "", fmt.Errorf("%w: scope %q", ErrInvalidInput, sc)
		}
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.now()) {
		return repo.APIKey{}, "", fmt.Errorf("%w: expiresAt", ErrInvalidInput)
	}
	token, prefix, secret := GenerateAPIKey()
	k, err := s.Store.CreateAPIKey(ctx, s.Actor(r, sp), repo.NewAPIKey{
		OrgID: orgID, Name: in.Name, Prefix: prefix, SecretHash: HashAPIKeySecret(s.Cfg.APIKeyPepper, secret),
		Scopes: in.Scopes, Role: repo.Role(in.Role), ExpiresAt: in.ExpiresAt,
	})
	if err != nil {
		return repo.APIKey{}, "", err
	}
	return k, token, nil
}

// RevokeAPIKey revokes a key.
func (s *Service) RevokeAPIKey(r *http.Request, p *Principal, orgID, keyID uuid.UUID) error {
	sp, err := s.Authorize(r.Context(), p, orgID, PermAPIKeysManage)
	if err != nil {
		return err
	}
	return s.Store.RevokeAPIKey(r.Context(), s.Actor(r, sp), orgID, keyID)
}

// DeleteOrg soft-deletes an org (owners only) and records it.
func (s *Service) DeleteOrg(r *http.Request, p *Principal, orgID uuid.UUID) error {
	sp, err := s.Authorize(r.Context(), p, orgID, PermOrgDelete)
	if err != nil {
		return err
	}
	return s.Store.DeleteOrg(r.Context(), s.Actor(r, sp), orgID)
}
