// Instance addons sit on top of instance secrets — they're just
// entries in the kuso-instance-shared Secret keyed
// INSTANCE_ADDON_<UPPER>_DSN_ADMIN. The dedicated
// /settings/instance-addons UI surfaces them as named connection
// records (with the host parsed out of the DSN for display) instead
// of raw env-var key/value rows.
//
// Why a separate code path: the raw instance-secrets endpoint
// returns keys-only, so a UI rendering a connection card needs
// host/port info parsed server-side (we never want to send the
// password back to the browser). Plus we want the UI to refuse to
// let an admin delete an addon that's currently in use by a
// project's KusoAddon spec.useInstanceAddon.

package instancesecrets

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	addonKeyPrefix = "INSTANCE_ADDON_"
	addonKeySuffix = "_DSN_ADMIN"
)

// InstanceAddon is the wire shape returned by ListInstanceAddons.
// Sensitive fields (password) are never sent — only the parsed
// host/port + the addon name. Kind is always "postgres" for v0.7.6;
// future kinds will read it from the DSN scheme.
type InstanceAddon struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port string `json:"port,omitempty"`
	User string `json:"user,omitempty"`
	Kind string `json:"kind"`
}

// AddonKeyForName returns the instance-secrets key that backs an
// addon registration. The on-the-wire name is lowercase but the
// key is uppercased per kube env-var convention.
func AddonKeyForName(name string) string {
	return addonKeyPrefix + strings.ToUpper(name) + addonKeySuffix
}

// addonNameFromKey is the inverse — strips the prefix/suffix and
// lowercases. Returns "" if the key isn't an addon key.
func addonNameFromKey(key string) string {
	if !strings.HasPrefix(key, addonKeyPrefix) || !strings.HasSuffix(key, addonKeySuffix) {
		return ""
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(key, addonKeyPrefix), addonKeySuffix)
	if mid == "" {
		return ""
	}
	return strings.ToLower(mid)
}

// ListInstanceAddons reads the instance-shared Secret and returns
// every entry whose key matches the INSTANCE_ADDON_*_DSN_ADMIN
// pattern, parsed into a display shape. Errors decoding individual
// DSNs don't abort — the entry surfaces with empty host/port so the
// admin can see + repair it.
func (s *Service) ListInstanceAddons(ctx context.Context) ([]InstanceAddon, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Get(ctx, SecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []InstanceAddon{}, nil
		}
		return nil, fmt.Errorf("read instance secret: %w", err)
	}
	out := []InstanceAddon{}
	for k, v := range sec.Data {
		name := addonNameFromKey(k)
		if name == "" {
			continue
		}
		entry := InstanceAddon{Name: name, Kind: "postgres"}
		if u, err := url.Parse(string(v)); err == nil {
			entry.Host = u.Hostname()
			entry.Port = u.Port()
			if u.User != nil {
				entry.User = u.User.Username()
			}
			if u.Scheme != "" {
				if u.Scheme == "postgres" || u.Scheme == "postgresql" {
					entry.Kind = "postgres"
				} else {
					entry.Kind = u.Scheme
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// RegisterInstanceAddon stores the admin DSN for a named instance
// addon. The name is normalised to lowercase + validated against
// kube-style identifier rules so the key it produces never collides
// with a regular instance secret.
func (s *Service) RegisterInstanceAddon(ctx context.Context, name, dsn string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !validAddonName(name) {
		return fmt.Errorf("%w: name must match [a-z][a-z0-9-]{0,30}", ErrInvalid)
	}
	if dsn == "" {
		return fmt.Errorf("%w: dsn required", ErrInvalid)
	}
	if _, err := url.Parse(dsn); err != nil {
		return fmt.Errorf("%w: dsn parse: %v", ErrInvalid, err)
	}
	return s.SetKey(ctx, AddonKeyForName(name), dsn)
}

// UnregisterInstanceAddon removes an instance addon registration.
// The caller should pre-check that no project currently uses it
// (the UI does this on the addon-list endpoint side); this method
// itself doesn't gate.
func (s *Service) UnregisterInstanceAddon(ctx context.Context, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !validAddonName(name) {
		return fmt.Errorf("%w: name must match [a-z][a-z0-9-]{0,30}", ErrInvalid)
	}
	return s.UnsetKey(ctx, AddonKeyForName(name))
}

// validAddonName mirrors the kube env-var-friendly subset of
// project/service names: must start with a letter, lowercase
// alnum + dash, ≤32 chars.
func validAddonName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !(r >= 'a' && r <= 'z') {
				return false
			}
			continue
		}
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return false
		}
	}
	return true
}
