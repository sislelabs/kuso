package backup

// Producer emits the restore shell for one addon kind and the metadata a
// backup records. Backup shell currently lives in the helm CronJob
// (backup-cronjob.yaml); this interface drives the Go-built restore Job
// and lets the handler ask "is this kind backable?".
type Producer interface {
	Kind() string        // addon kind, e.g. "mongodb"
	PayloadKind() string // manifest payloadKind, e.g. "mongodump"
	ArtifactExt() string // artifact key suffix, e.g. "archive.gz"
	RestoreScript() string
}

// ManualRestorer is implemented by producers whose artifacts are backed up
// but must be restored by hand; the restore API refuses them instead of
// starting a Job whose script can only exit 1.
type ManualRestorer interface {
	ManualRestore() bool
}
