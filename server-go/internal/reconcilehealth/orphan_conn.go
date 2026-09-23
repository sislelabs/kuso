package reconcilehealth

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// KindOrphanConnSecret: a "<addon>-conn" Secret exists with no KusoAddon
// behind it. The addon was deleted but its credential survived — and for
// an instance-pg addon, the logical database that credential names is
// still sitting on the shared server consuming space.
//
// This is the fingerprint of a leaked addon, and it is specifically how
// 16 databases (~155MB) hid on the production cluster: each orphan still
// had a plausible-looking conn Secret, so any check that asked "is
// anything referencing this database?" answered yes. Detection must key
// off the ABSENCE of the owning CR instead.
const KindOrphanConnSecret Kind = "orphan_conn_secret"

const connSecretSuffix = "-conn"

// detectOrphanConnSecrets reports every conn Secret whose addon CR is
// gone. live maps addon CR names ("<project>-<addon>") that still exist.
//
// Pure and allocation-light so it can run on every health tick: the
// caller supplies both lists, which keeps this testable without a kube
// client.
func detectOrphanConnSecrets(secrets []corev1.Secret, live map[string]bool) []Issue {
	var out []Issue
	for i := range secrets {
		sec := &secrets[i]
		name := sec.Name
		if !strings.HasSuffix(name, connSecretSuffix) {
			continue
		}
		addonCR := strings.TrimSuffix(name, connSecretSuffix)
		if addonCR == "" || live[addonCR] {
			continue
		}
		out = append(out, Issue{
			Resource:  name,
			Namespace: sec.Namespace,
			Project:   sec.Labels["kuso.sislelabs.com/project"],
			Type:      "addon",
			Kind:      KindOrphanConnSecret,
			Severity:  SeverityWarning,
			Summary: fmt.Sprintf(
				"Secret %s has no KusoAddon %q — the addon was deleted but its credential survived.",
				name, addonCR),
			Detail: "For an instance-pg addon the logical database this credential names is " +
				"also still present on the shared server, consuming space with nothing " +
				"referencing it. Confirm the data is not needed (back it up first), then " +
				"drop the database and remove this Secret.",
			// Never Safe: auto-deleting a credential can strand a database
			// an operator still wants to recover, and the data is
			// unrecoverable once dropped. Surface it; let a human decide.
			Action: ActionNone,
			Safe:   false,
			Fix: "Back up the database, then drop it and delete this Secret. " +
				"If the addon should still exist, re-create it instead.",
			RunbookCmd: fmt.Sprintf(
				"kubectl get secret %s -n %s -o jsonpath='{.data.POSTGRES_DB}' | base64 -d  "+
					"# then: pg_dump that DB, DROP DATABASE it, and "+
					"kubectl delete secret %s -n %s",
				name, sec.Namespace, name, sec.Namespace),
		})
	}
	return out
}
