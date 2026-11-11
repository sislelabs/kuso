package addons

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/lib/pq"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// adminSecretSuffix names the Secret holding an appRole addon's superuser
// password (kusoaddon.adminSecret in the chart).
const adminSecretSuffix = "-admin"

// newAddonGetsAppRole: only a native single-node postgres or a mysql that
// kuso provisions itself gets the non-superuser app role. Env-scoped clones
// (previews, env groups) are seeded from a dump of their source, which may
// need superuser to replay, so they keep the legacy shape.
func newAddonGetsAppRole(req CreateAddonRequest, isExternal bool) bool {
	if isExternal || req.UseInstanceAddon != "" || req.ExtraLabels[kube.LabelEnv] != "" {
		return false
	}
	switch req.Kind {
	case "postgres":
		return !req.HA
	case "mysql":
		return true
	}
	return false
}

// DBRotateResult is what a password rotation reports back.
type DBRotateResult struct {
	User string `json:"user"`
	// RootRotated: a mysql addon created before appRole shared its root
	// password with the app user, so root moved to the new password too.
	RootRotated bool   `json:"rootRotated"`
	Message     string `json:"message"`
}

// dbPasswordKeys lists, per kind, the conn Secret key holding the password
// and the URL keys that embed it (postgres.yaml / mysql.yaml).
var dbPasswordKeys = map[string]struct {
	user, pass string
	urls       []string
}{
	"postgres": {"POSTGRES_USER", "POSTGRES_PASSWORD", []string{"DATABASE_URL", "DIRECT_URL", "POOLER_URL"}},
	"mysql":    {"MYSQL_USER", "MYSQL_PASSWORD", []string{"MYSQL_URL", "DATABASE_URL"}},
}

// rotatedConnData returns conn data with the password swapped for newPw in
// the password key and in every URL key that carries it. A URL key that is
// set but does not embed the current password means the Secret has drifted
// from what the chart renders; refusing then beats writing half a rotation.
func rotatedConnData(kind string, data map[string][]byte, newPw string) (map[string][]byte, error) {
	keys, ok := dbPasswordKeys[kind]
	if !ok {
		return nil, fmt.Errorf("%w: password rotation supports postgres and mysql", ErrInvalid)
	}
	oldPw := string(data[keys.pass])
	if oldPw == "" || len(data[keys.user]) == 0 {
		return nil, fmt.Errorf("%w: conn secret lacks %s/%s", ErrInvalid, keys.user, keys.pass)
	}
	out := make(map[string][]byte, len(data))
	for k, v := range data {
		out[k] = v
	}
	out[keys.pass] = []byte(newPw)
	oldFrag := ":" + url.QueryEscape(oldPw) + "@"
	newFrag := ":" + url.QueryEscape(newPw) + "@"
	for _, k := range keys.urls {
		v := string(data[k])
		if v == "" {
			continue
		}
		if !strings.Contains(v, oldFrag) {
			return nil, fmt.Errorf("%w: %s in the conn secret does not carry the current password; run repair-password or recreate the key before rotating", ErrConflict, k)
		}
		out[k] = []byte(strings.Replace(v, oldFrag, newFrag, 1))
	}
	return out, nil
}

// RotateDBPassword gives a native postgres or mysql addon's app user a new
// password: the database first, then the conn Secret, then a restart of
// the pooler and of every env reading the Secret. If the Secret write
// fails the database is set back, so the two never disagree.
func (s *Service) RotateDBPassword(ctx context.Context, project, name string) (*DBRotateResult, error) {
	a, err := s.GetOwned(ctx, project, name)
	if err != nil {
		return nil, err
	}
	if a.Spec.External != nil || a.Spec.UseInstanceAddon != "" {
		return nil, fmt.Errorf("%w: external and instance-shared addons rotate through resync-external / resync-instance", ErrInvalid)
	}
	switch {
	case a.Spec.Kind == "postgres" && a.Spec.HA:
		return nil, fmt.Errorf("%w: password rotation does not cover HA (CloudNativePG) postgres yet", ErrInvalid)
	case a.Spec.Kind == "postgres" && a.Spec.PasswordSecret != nil:
		return nil, fmt.Errorf("%w: this addon takes its password from Secret %s; rotate that Secret, then run repair-password", ErrInvalid, a.Spec.PasswordSecret.Name)
	case a.Spec.Kind != "postgres" && a.Spec.Kind != "mysql":
		return nil, fmt.Errorf("%w: password rotation supports postgres and mysql, not %s", ErrInvalid, a.Spec.Kind)
	}
	ns := s.nsFor(ctx, project)
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	conn, err := secrets.Get(ctx, connSecretName(a.Name), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read conn secret: %w", err)
	}
	newPw, err := randString(32)
	if err != nil {
		return nil, err
	}
	data, err := rotatedConnData(a.Spec.Kind, conn.Data, newPw)
	if err != nil {
		return nil, err
	}
	keys := dbPasswordKeys[a.Spec.Kind]
	user := string(conn.Data[keys.user])
	oldPw := string(conn.Data[keys.pass])

	// A mysql addon from before appRole has no admin Secret: its root shares
	// the app password, so a leaked DSN is a root credential. Root moves with
	// the app user there; otherwise the old password would stay a way in.
	legacyMySQLRoot := false
	if a.Spec.Kind == "mysql" {
		if _, err := secrets.Get(ctx, a.Name+adminSecretSuffix, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			legacyMySQLRoot = true
		} else if err != nil {
			return nil, fmt.Errorf("read admin secret: %w", err)
		}
	}

	set := func(pw, authPw string) error {
		if a.Spec.Kind == "postgres" {
			return s.alterPostgresPassword(ctx, ns, a.Name, user, pw)
		}
		return s.alterMySQLPassword(ctx, ns, a.Name, user, authPw, pw, legacyMySQLRoot)
	}
	if err := set(newPw, oldPw); err != nil {
		return nil, err
	}
	upd := conn.DeepCopy()
	upd.Data = data
	if _, err := secrets.Update(ctx, upd, metav1.UpdateOptions{}); err != nil {
		if rerr := set(oldPw, newPw); rerr != nil {
			return nil, fmt.Errorf("write conn secret: %w; setting the old password back also failed (%v): the database now expects a password %s does not hold, run repair-password", err, rerr, conn.Name)
		}
		return nil, fmt.Errorf("write conn secret (the database password was set back, nothing changed): %w", err)
	}
	s.restartPooler(ctx, ns, a.Name)
	s.restartConnConsumers(ctx, ns, conn.Name)

	res := &DBRotateResult{User: user, RootRotated: legacyMySQLRoot}
	res.Message = fmt.Sprintf("%s has a new password in %s; services using it are restarting. Sessions opened with the old password stay connected until they close.", user, conn.Name)
	if legacyMySQLRoot {
		res.Message += " This addon predates separate root credentials, so root moved to the new password too."
	}
	return res, nil
}

// alterPostgresPassword runs ALTER ROLE inside the addon pod over the local
// trust socket, as the pod's superuser (postgresExecTarget), so it works
// whatever password the server currently expects.
func (s *Service) alterPostgresPassword(ctx context.Context, ns, fqn, user, pw string) error {
	pod, psqlUser, err := s.postgresExecTarget(ctx, ns, fqn, user)
	if err != nil {
		return err
	}
	esc := strings.ReplaceAll(pw, "'", "''")
	stmt := fmt.Sprintf(`ALTER ROLE %s WITH PASSWORD '%s'`, pq.QuoteIdentifier(user), esc)
	_, stderr, err := s.execInPod(ctx, ns, pod, "postgres", []string{
		"psql", "-v", "ON_ERROR_STOP=1", "-U", psqlUser, "-d", "postgres", "-h", "/var/run/postgresql", "-c", stmt,
	})
	if err != nil {
		return fmt.Errorf("set postgres password: %v: %s", err, redactSecret(stderr, pw, esc))
	}
	return nil
}

// alterMySQLPassword authenticates as the app user with authPw (MySQL lets
// an account change its own password, so no root credential is needed) and
// sets pw. withRoot also moves root, for addons whose root shares it.
func (s *Service) alterMySQLPassword(ctx context.Context, ns, fqn, user, authPw, pw string, withRoot bool) error {
	lit := func(v string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), "'", `\'`) + "'"
	}
	authUser := user
	stmt := "ALTER USER CURRENT_USER() IDENTIFIED BY " + lit(pw)
	if withRoot {
		authUser = "root"
		stmt = fmt.Sprintf("ALTER USER %s@'%%' IDENTIFIED BY %[2]s; ALTER USER IF EXISTS 'root'@'%%' IDENTIFIED BY %[2]s; ALTER USER IF EXISTS 'root'@'localhost' IDENTIFIED BY %[2]s",
			lit(user), lit(pw))
	}
	// The password travels as a positional argument, not inside the -c
	// script, so it never needs shell quoting.
	_, stderr, err := s.execInPod(ctx, ns, fqn+"-0", "mysql", []string{
		"sh", "-c", `MYSQL_PWD="$1" mysql -h 127.0.0.1 -u "$2" -e "$3"`, "sh", authPw, authUser, stmt,
	})
	if err != nil {
		return fmt.Errorf("set mysql password: %v: %s", err, redactSecret(stderr, pw, authPw))
	}
	return nil
}

// restartPooler rolls the PgBouncer Deployment, whose userlist is rendered
// from the conn Secret at pod start. Absent pooler is fine.
func (s *Service) restartPooler(ctx context.Context, ns, fqn string) {
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{"kuso.sislelabs.com/restartedAt":%q}}}}}`, time.Now().UTC().Format(time.RFC3339))
	_, err := s.Kube.Clientset.AppsV1().Deployments(ns).Patch(ctx, fqn+"-pooler", types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		slog.Default().Warn("addons: restart pooler after password rotation", "addon", fqn, "err", err)
	}
}
