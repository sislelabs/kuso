package kube

// AddonBackupCronJobRendered mirrors the render conditions of the six
// backup-CronJob variants in
// operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml. The
// health surfaces (reconcilehealth scanner, backuphealth watcher) must
// agree with the chart about WHICH addons get a `<name>-backup`
// CronJob at all — expecting one where the chart deliberately renders
// none (instance-shared addons → backed up via the instance addon;
// unsupported kinds) produced permanent false "backups failing" pages.
// HA postgres does get one: it pg_dumps from the CNPG <name>-rw Service.
func AddonBackupCronJobRendered(a *KusoAddon) bool {
	if a == nil || a.Spec.Backup == nil || a.Spec.Backup.Schedule == "" {
		return false
	}
	// Helm truthiness, not Go nil-ness: the chart tests
	// `.Values.external`, and an EMPTY map is falsy to helm — an
	// `external: {}` written via kubectl/config-as-code renders the
	// normal CronJob. Mirror that exactly.
	external := a.Spec.External != nil &&
		(a.Spec.External.SecretName != "" || len(a.Spec.External.SecretKeys) > 0)
	switch a.Spec.Kind {
	case "postgres":
		if external {
			return true // external-postgres variant has no ha/instance gate
		}
		return a.Spec.UseInstanceAddon == ""
	case "redis", "mongodb", "mysql", "s3":
		return !external && a.Spec.UseInstanceAddon == ""
	default:
		return false
	}
}
