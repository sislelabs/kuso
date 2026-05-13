// Per-domain repository views over *DB.
//
// Background: *db.DB grew to 140 methods spanning users, tokens,
// notifications, alerts, builds, github, invites, login attempts,
// ssh keys, revisions, settings, … Every HTTP handler historically
// took *db.DB and so received the union of every domain's surface —
// "find usages on Method" returned noise, test fakes had to stub all
// 140 methods, and a handler with no reason to touch users could call
// FindUserByEmail by accident.
//
// Solution: typed views. Each view is a one-field struct holding
// *DB and exposing only the methods of one domain. Existing methods
// on *DB are kept (so old handlers don't break) and the view's
// methods delegate to them. Handlers that want the tight surface
// take the view; handlers we haven't migrated yet keep *db.DB.
//
// This is the "approach 1" from the P0-2 architecture review — an
// incremental migration that lands the structural shape today and
// lets us narrow handler signatures one at a time without one
// risky mega-commit.

package db

import (
	"context"
	"time"
)

// ---- UsersRepo ------------------------------------------------------

// UsersRepo exposes only the user-management methods on *DB. Handlers
// that operate on users (auth login, user CRUD, password reset) can
// take *UsersRepo instead of *db.DB so their test fakes only stub
// what they actually call.
type UsersRepo struct{ db *DB }

// Users returns the users view. The returned value shares the same
// *sql.DB as the parent so transactions, write counters, and tenancy
// cache all stay coherent.
func (d *DB) Users() *UsersRepo { return &UsersRepo{db: d} }

func (r *UsersRepo) FindByUsername(ctx context.Context, username string) (*User, error) {
	return r.db.FindUserByUsername(ctx, username)
}
func (r *UsersRepo) FindByEmail(ctx context.Context, email string) (*User, error) {
	return r.db.FindUserByEmail(ctx, email)
}
func (r *UsersRepo) FindByID(ctx context.Context, id string) (*User, error) {
	return r.db.FindUserByID(ctx, id)
}
func (r *UsersRepo) UpdateLogin(ctx context.Context, userID, ip string, when time.Time) error {
	return r.db.UpdateUserLogin(ctx, userID, ip, when)
}
func (r *UsersRepo) Permissions(ctx context.Context, userID string) ([]string, error) {
	return r.db.UserPermissions(ctx, userID)
}
func (r *UsersRepo) RoleName(ctx context.Context, userID string) (string, error) {
	return r.db.UserRoleName(ctx, userID)
}
func (r *UsersRepo) GroupNames(ctx context.Context, userID string) ([]string, error) {
	return r.db.UserGroupNames(ctx, userID)
}
func (r *UsersRepo) Create(ctx context.Context, in CreateUserInput) error {
	return r.db.CreateUser(ctx, in)
}
func (r *UsersRepo) Update(ctx context.Context, id string, in UpdateUserInput) error {
	return r.db.UpdateUser(ctx, id, in)
}
func (r *UsersRepo) Delete(ctx context.Context, id string) error {
	return r.db.DeleteUser(ctx, id)
}
func (r *UsersRepo) UpdatePassword(ctx context.Context, id, hash string) error {
	return r.db.UpdateUserPassword(ctx, id, hash)
}

// ---- TokensRepo -----------------------------------------------------

// TokensRepo exposes only the API-token / session-token surface. Auth
// middleware + token-CRUD endpoints are the natural consumers.
type TokensRepo struct{ db *DB }

// Tokens returns the tokens view.
func (d *DB) Tokens() *TokensRepo { return &TokensRepo{db: d} }

func (r *TokensRepo) Create(ctx context.Context, t *Token) error {
	return r.db.CreateToken(ctx, t)
}
func (r *TokensRepo) ListForUser(ctx context.Context, userID string) ([]Token, error) {
	return r.db.ListTokensForUser(ctx, userID)
}
func (r *TokensRepo) DeleteForUser(ctx context.Context, userID, tokenID string) error {
	return r.db.DeleteUserToken(ctx, userID, tokenID)
}
func (r *TokensRepo) ListAll(ctx context.Context) ([]AdminToken, error) {
	return r.db.ListAllTokens(ctx)
}
func (r *TokensRepo) Delete(ctx context.Context, id string) error {
	return r.db.DeleteToken(ctx, id)
}

// ---- NotificationsRepo ----------------------------------------------

// NotificationsRepo exposes only the in-app notification feed +
// webhook-target methods. The bell-icon endpoint and the notify
// dispatcher are the only callers.
type NotificationsRepo struct{ db *DB }

// Notifications returns the notifications view.
func (d *DB) Notifications() *NotificationsRepo { return &NotificationsRepo{db: d} }

func (r *NotificationsRepo) ListTargets(ctx context.Context) ([]Notification, error) {
	return r.db.ListNotifications(ctx)
}
func (r *NotificationsRepo) FindTarget(ctx context.Context, id string) (*Notification, error) {
	return r.db.FindNotification(ctx, id)
}
func (r *NotificationsRepo) CreateTarget(ctx context.Context, n *Notification) error {
	return r.db.CreateNotification(ctx, n)
}
func (r *NotificationsRepo) UpdateTarget(ctx context.Context, n *Notification) error {
	return r.db.UpdateNotification(ctx, n)
}
func (r *NotificationsRepo) DeleteTarget(ctx context.Context, id string) error {
	return r.db.DeleteNotification(ctx, id)
}
func (r *NotificationsRepo) PruneEvents(ctx context.Context, before time.Time) (int64, error) {
	return r.db.PruneNotificationEvents(ctx, before)
}
func (r *NotificationsRepo) InsertEvent(ctx context.Context, e NotificationEvent) error {
	return r.db.InsertNotificationEvent(ctx, e)
}
func (r *NotificationsRepo) ListEvents(ctx context.Context, limit int, unreadOnly bool) ([]NotificationEvent, error) {
	return r.db.ListNotificationEvents(ctx, limit, unreadOnly)
}
func (r *NotificationsRepo) ListEventsForProjects(ctx context.Context, limit int, projects []string) ([]NotificationEvent, error) {
	return r.db.ListNotificationEventsForProjects(ctx, limit, projects)
}
func (r *NotificationsRepo) CountUnreadEvents(ctx context.Context) (int, error) {
	return r.db.CountUnreadNotificationEvents(ctx)
}
func (r *NotificationsRepo) MarkAllEventsRead(ctx context.Context) error {
	return r.db.MarkAllNotificationEventsRead(ctx)
}
func (r *NotificationsRepo) ClearAllEvents(ctx context.Context) (int64, error) {
	return r.db.ClearAllNotificationEvents(ctx)
}
