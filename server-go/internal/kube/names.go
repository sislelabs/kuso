package kube

import (
	"errors"
	"fmt"
)

// MaxReleaseNameLen is helm's release-name limit. The helm-operator
// names each release after its CR, so a CR name longer than this
// never renders and its reconcile fails forever.
const MaxReleaseNameLen = 53

// MaxLabelValueLen is the kube label-value limit (also the DNS-1123
// label limit Services and StatefulSet pod hostnames are held to).
const MaxLabelValueLen = 63

// ErrNameTooLong is returned when a derived kube object name exceeds
// a kube or helm limit. Handlers map it to 400.
var ErrNameTooLong = errors.New("name too long")

// ValidateReleaseName rejects a CR / helm release name that helm
// would refuse to install.
func ValidateReleaseName(name string) error {
	if len(name) > MaxReleaseNameLen {
		return fmt.Errorf("%w: %q is %d characters; kubernetes resource names derived from it must be at most %d — use a shorter name",
			ErrNameTooLong, name, len(name), MaxReleaseNameLen)
	}
	return nil
}

// ValidateLabelValue rejects a value that cannot be stored as a kube
// label value or a DNS-1123 label (Service name, pod hostname).
func ValidateLabelValue(value string) error {
	if len(value) > MaxLabelValueLen {
		return fmt.Errorf("%w: %q is %d characters; must be at most %d",
			ErrNameTooLong, value, len(value), MaxLabelValueLen)
	}
	return nil
}
