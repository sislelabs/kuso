// Instance-addon provisioner — Model 2 from the v0.7.6 design.
//
// The admin registers a shared database server by storing a
// superuser DSN in the kuso-instance-shared Secret under a key
// like INSTANCE_ADDON_PG_DSN_ADMIN. Projects opt in by creating a
// regular KusoAddon with spec.useInstanceAddon = "pg" — the kuso
// server then:
//
//   1. Reads the admin DSN.
//   2. CREATE DATABASE "<project>_<addon>" (idempotent).
//   3. CREATE USER "<project>_<addon>" with a generated password.
//   4. GRANT ALL on the new DB.
//   5. Writes the per-project DSN into <addon>-conn.
//
// Why Postgres-only for v0.7.6: Redis "isolation" on a shared server
// means key-prefixing in the app, not a separate logical instance —
// no provisioning step actually applies. We can revisit if a Redis
// shop asks.

package addons

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/lib/pq"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// instanceAdminDSN reads INSTANCE_ADDON_<UPPER>_DSN_ADMIN out of
// the kuso-instance-shared Secret. The admin sets it via the
// /settings/instance-secrets page or `kuso instance-secret set`.
func (s *Service) instanceAdminDSN(ctx context.Context, instanceAddonName string) (string, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Get(ctx, "kuso-instance-shared", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("%w: instance secrets not configured (set INSTANCE_ADDON_%s_DSN_ADMIN)", ErrInvalid, strings.ToUpper(instanceAddonName))
		}
		return "", fmt.Errorf("read instance secrets: %w", err)
	}
	key := fmt.Sprintf("INSTANCE_ADDON_%s_DSN_ADMIN", strings.ToUpper(instanceAddonName))
	v, ok := sec.Data[key]
	if !ok || len(v) == 0 {
		return "", fmt.Errorf("%w: instance addon %q not registered (admin must set %s)", ErrInvalid, instanceAddonName, key)
	}
	return string(v), nil
}

// provisionInstanceAddonDB creates the per-project database + user
// on the shared server pointed to by adminDSN, then returns the
// per-project DSN that should be stored in <addon>-conn. dbName /
// userName are the shared form "<project>_<addon>" — both bounded
// by 63 chars (Postgres limit).
func (s *Service) provisionInstanceAddonDB(adminDSN, project, addonShort string) (perProjectDSN, password string, err error) {
	dbName := pgIdentifier(project, addonShort)
	userName := dbName

	pw, err := randPassword()
	if err != nil {
		return "", "", fmt.Errorf("gen password: %w", err)
	}

	db, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return "", "", fmt.Errorf("open admin: %w", err)
	}
	defer db.Close()

	// CREATE DATABASE doesn't run inside a tx; use IF NOT EXISTS via
	// a pg_database lookup. We can't parameterize identifiers, so we
	// quote them safely.
	if _, err := db.Exec(fmt.Sprintf(`SELECT 1 FROM pg_database WHERE datname = '%s'`, escapeLiteral(dbName))); err == nil {
		var exists int
		row := db.QueryRow(fmt.Sprintf(`SELECT 1 FROM pg_database WHERE datname = '%s'`, escapeLiteral(dbName)))
		_ = row.Scan(&exists)
		if exists != 1 {
			if _, err := db.Exec(fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
				return "", "", fmt.Errorf("create db %s: %w", dbName, err)
			}
		}
	}

	// User: create-or-rotate. We always issue ALTER ROLE … PASSWORD
	// so a fresh provision and a re-provision both end with a known
	// password we can return to the caller.
	var userExists int
	_ = db.QueryRow(fmt.Sprintf(`SELECT 1 FROM pg_roles WHERE rolname = '%s'`, escapeLiteral(userName))).Scan(&userExists)
	if userExists != 1 {
		if _, err := db.Exec(fmt.Sprintf(`CREATE ROLE %q WITH LOGIN PASSWORD '%s'`, userName, escapeLiteral(pw))); err != nil {
			return "", "", fmt.Errorf("create role: %w", err)
		}
	} else {
		if _, err := db.Exec(fmt.Sprintf(`ALTER ROLE %q WITH LOGIN PASSWORD '%s'`, userName, escapeLiteral(pw))); err != nil {
			return "", "", fmt.Errorf("rotate role password: %w", err)
		}
	}

	if _, err := db.Exec(fmt.Sprintf(`GRANT ALL PRIVILEGES ON DATABASE %q TO %q`, dbName, userName)); err != nil {
		return "", "", fmt.Errorf("grant: %w", err)
	}

	// Build per-project DSN by swapping the database + auth on the
	// admin DSN. Keeps host / port / sslmode etc. intact.
	u, err := url.Parse(adminDSN)
	if err != nil {
		return "", "", fmt.Errorf("parse admin DSN: %w", err)
	}
	u.User = url.UserPassword(userName, pw)
	u.Path = "/" + dbName
	return u.String(), pw, nil
}

// writeInstanceAddonConnSecret writes (or updates) the addon's
// <name>-conn secret with DATABASE_URL + the broken-out fields.
// Same shape as the kusoaddon postgres chart's conn secret so
// services envFrom: it transparently.
func (s *Service) writeInstanceAddonConnSecret(ctx context.Context, ns, addonFQN, dsn, password string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("parse per-project DSN: %w", err)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	user := u.User.Username()
	dbName := strings.TrimPrefix(u.Path, "/")
	data := map[string][]byte{
		"DATABASE_URL":      []byte(dsn),
		"POSTGRES_HOST":     []byte(host),
		"POSTGRES_PORT":     []byte(port),
		"POSTGRES_USER":     []byte(user),
		"POSTGRES_PASSWORD": []byte(password),
		"POSTGRES_DB":       []byte(dbName),
	}
	connName := connSecretName(addonFQN)
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      connName,
			Namespace: ns,
			Labels: map[string]string{
				"kuso.sislelabs.com/addon-conn":      "true",
				"kuso.sislelabs.com/instance-shared": "true",
			},
		},
		Data: data,
	}
	if existing, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, connName, metav1.GetOptions{}); err == nil {
		existing.Data = data
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range dst.Labels {
			existing.Labels[k] = v
		}
		if _, err := s.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update conn secret: %w", err)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("preflight conn secret: %w", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, dst, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create conn secret: %w", err)
	}
	return nil
}

// pgIdentifier builds a Postgres-safe DB / role name. Both project
// and addon names are already constrained to [a-z0-9-]+ at the API
// level, so we just join with an underscore and replace dashes with
// underscores. Bounded to 63 chars.
func pgIdentifier(project, addon string) string {
	id := strings.ReplaceAll(project+"_"+addon, "-", "_")
	if len(id) > 63 {
		id = id[:63]
	}
	return id
}

// randPassword returns 24 random hex chars (96 bits of entropy).
func randPassword() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// escapeLiteral does the minimal single-quote / backslash escaping
// for embedding a string literal into a CREATE / ALTER ROLE
// statement. We don't have a parameterized form for these because
// PASSWORD '…' is a literal — not a parameter binding. The caller
// owns name validation upstream.
func escapeLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return s
}

// ResyncInstanceAddon re-runs the provisioner for an instance-shared
// addon. Useful if the per-project DSN secret was deleted, or to
// rotate the password.
func (s *Service) ResyncInstanceAddon(ctx context.Context, project, name string) error {
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, name)
	addon, err := s.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, name)
		}
		return fmt.Errorf("get addon: %w", err)
	}
	if addon.Spec.UseInstanceAddon == "" {
		return fmt.Errorf("%w: addon %s/%s does not use an instance addon", ErrInvalid, project, name)
	}
	adminDSN, err := s.instanceAdminDSN(ctx, addon.Spec.UseInstanceAddon)
	if err != nil {
		return err
	}
	short := ShortName(project, fqn)
	dsn, pw, err := s.provisionInstanceAddonDB(adminDSN, project, short)
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	return s.writeInstanceAddonConnSecret(ctx, ns, fqn, dsn, pw)
}
