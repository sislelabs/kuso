package backup

import "strings"

// Every key Secret the kusoaddon chart renders carries this label, which
// is how export, import and health find them across namespaces.
const (
	KeyRoleLabel = "kuso.sislelabs.com/role"
	KeyRoleValue = "backup-key"
	// KeyExportedAtAnnotation is stamped on a key Secret each time it is
	// exported, so health can say which keys still exist only in-cluster.
	KeyExportedAtAnnotation = "kuso.sislelabs.com/backup-key-exported-at"
	keySecretSuffix         = "-backup-key"
)

// KeyRoleSelector is the label selector for every backup-key Secret.
func KeyRoleSelector() string { return KeyRoleLabel + "=" + KeyRoleValue }

// ReleaseForKeySecret returns the addon helm release (the addon CR name) a
// key Secret belongs to, and false for a name that is not a key Secret.
func ReleaseForKeySecret(secret string) (string, bool) {
	rel, ok := strings.CutSuffix(secret, keySecretSuffix)
	return rel, ok && rel != ""
}
