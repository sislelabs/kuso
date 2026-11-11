package backup

// --- s3 (MinIO) ---------------------------------------------------------

// s3Producer restores an s3 addon snapshot: the <stamp>/ prefix (or a
// <stamp>.bucket-<name>/ sibling) the backup CronJob copied the addon's
// buckets into. Snapshots written with a backup key are rclone-crypt
// encrypted and say so in .kuso-snapshot.manifest.json (BLOB-6); older
// snapshots have no manifest and are plaintext.
type s3Producer struct{}

func (s3Producer) Kind() string        { return "s3" }
func (s3Producer) PayloadKind() string { return "s3-snapshot" }
func (s3Producer) ArtifactExt() string { return "" }

// S3SnapshotRestoreScript is exported for the real-image test.
const S3SnapshotRestoreScript = `
set -eu
# KEY is any key inside a snapshot (the list shows objects): reduce it to
# <project>/<addon>/<snapshot dir>.
rest=${KEY#*/}; rest=${rest#*/}
seg=${rest%%/*}
case "${seg}" in
  [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z) TB="${TGT_BUCKET}" ;;
  [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z.bucket-?*) TB="${seg#*.bucket-}" ;;
  *) echo "==> ${KEY} is not inside an s3 snapshot" >&2; exit 1 ;;
esac
SNAP="${KEY%"${rest}"}${seg}"
export RCLONE_CONFIG_BK_TYPE=s3 RCLONE_CONFIG_BK_PROVIDER=Other
export RCLONE_CONFIG_BK_ENDPOINT="${S3_ENDPOINT}" RCLONE_CONFIG_BK_REGION="${AWS_DEFAULT_REGION:-us-east-1}"
export RCLONE_CONFIG_BK_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID}" RCLONE_CONFIG_BK_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY}"
export RCLONE_CONFIG_TGT_TYPE=s3 RCLONE_CONFIG_TGT_PROVIDER=Other RCLONE_CONFIG_TGT_ENDPOINT="${TGT_ENDPOINT}"
export RCLONE_CONFIG_TGT_ACCESS_KEY_ID="${TGT_ACCESS_KEY}" RCLONE_CONFIG_TGT_SECRET_ACCESS_KEY="${TGT_SECRET_KEY}"
# A fresh pod has no network until the NetworkPolicy for its IP syncs:
# retry until the snapshot lists, then until the addon's MinIO answers.
_i=0
until LISTING=$(rclone lsf "bk:${BUCKET}/${SNAP}/" --max-depth 1 2>/dev/null) && [ -n "${LISTING}" ]; do
  _i=$((_i+1))
  if [ "${_i}" -ge 30 ]; then echo "==> snapshot s3://${BUCKET}/${SNAP}/ not found or unreachable after 60s" >&2; exit 1; fi
  sleep 2
done
_i=0
until rclone lsd tgt: >/dev/null 2>&1; do
  _i=$((_i+1))
  if [ "${_i}" -ge 30 ]; then echo "==> MinIO ${TGT_ENDPOINT} unreachable after 60s" >&2; exit 1; fi
  sleep 2
done
SRC="bk:${BUCKET}/${SNAP}"
if MANIFEST=$(rclone cat "bk:${BUCKET}/${SNAP}/.kuso-snapshot.manifest.json" 2>/dev/null) && case "${MANIFEST}" in *'"encryption"'*) true ;; *) false ;; esac; then
  if [ -z "${BACKUP_ENCRYPTION_KEY:-}" ]; then
    echo "==> snapshot is encrypted but the addon's backup key is missing; cannot restore" >&2
    exit 1
  fi
  export RCLONE_CONFIG_ENC_TYPE=crypt RCLONE_CONFIG_ENC_REMOTE="bk:${BUCKET}/${SNAP}"
  export RCLONE_CONFIG_ENC_FILENAME_ENCRYPTION=off RCLONE_CONFIG_ENC_DIRECTORY_NAME_ENCRYPTION=false
  RCLONE_CONFIG_ENC_PASSWORD=$(rclone obscure "${BACKUP_ENCRYPTION_KEY}")
  export RCLONE_CONFIG_ENC_PASSWORD
  SRC="enc:"
  echo "==> decrypting snapshot ${SNAP}"
else
  echo "==> snapshot ${SNAP} is not encrypted (taken before encryption, or without a key)"
fi
rclone mkdir "tgt:${TB}"
echo "==> copying ${SNAP} into bucket ${TB} (existing objects of the same name are overwritten; others are kept)"
rclone copy "${SRC}" "tgt:${TB}" --exclude "/.kuso-snapshot.manifest.json" \
  --transfers 16 --checkers 16 --retries 3 --s3-no-check-bucket --stats-one-line --stats 30s
echo "==> done"
`

func (s3Producer) RestoreScript() string { return S3SnapshotRestoreScript }
