package kube

// AddonBackupCronJobRendered mirrors the render conditions of the six
// backup-CronJob variants in
// operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml. The
// health surfaces (reconcilehealth scanner, backuphealth watcher) must
// agree with the chart about WHICH addons get a `<name>-backup`
// CronJob at all — expecting one where the chart deliberately renders
// none (HA postgres → CNPG barman; instance-shared addons → backed up
// via the instance addon; unsupported kinds) produced permanent false
// "backups failing" pages.
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
		return !a.Spec.HA && a.Spec.UseInstanceAddon == ""
	case "redis", "mongodb", "mysql", "s3":
		return !external && a.Spec.UseInstanceAddon == ""
	default:
		return false
	}
}

// AddonBackupSuppressedHA reports the one genuinely dangerous
// non-rendered combination: a backup schedule on an HA postgres. The
// chart suppresses the pg_dump CronJob there (CNPG barman would
// conflict) but kuso does NOT plumb barman itself — so unless the
// operator configured CNPG backups out-of-band, this addon LOOKS
// scheduled and has zero backups.
func AddonBackupSuppressedHA(a *KusoAddon) bool {
	if a == nil || a.Spec.Backup == nil || a.Spec.Backup.Schedule == "" {
		return false
	}
	external := a.Spec.External != nil &&
		(a.Spec.External.SecretName != "" || len(a.Spec.External.SecretKeys) > 0)
	return a.Spec.Kind == "postgres" && a.Spec.HA &&
		!external && a.Spec.UseInstanceAddon == ""
}
