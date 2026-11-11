package notify

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"kuso/server/internal/db"
)

// The direct fallback pages from the last channel list this process read.
// A kuso-server that starts while the control-plane Postgres is down has
// never read one, so the list is also kept in a Secret (it carries webhook
// URLs and bot tokens) and loaded at boot.

// ChannelSnapshotStore keeps the fallback channel list outside Postgres.
type ChannelSnapshotStore interface {
	Load(ctx context.Context) ([]byte, error)
	Save(ctx context.Context, data []byte) error
}

const (
	// FallbackChannelsSecret is the Secret SecretChannelSnapshot uses.
	FallbackChannelsSecret = "kuso-notify-fallback"
	fallbackChannelsKey    = "channels.json"
	snapshotSaveTimeout    = 10 * time.Second
)

// SecretChannelSnapshot stores the snapshot in a Secret in Namespace.
type SecretChannelSnapshot struct {
	Client    kubernetes.Interface
	Namespace string
}

func (s *SecretChannelSnapshot) Load(ctx context.Context) ([]byte, error) {
	sec, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, FallbackChannelsSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sec.Data[fallbackChannelsKey], nil
}

func (s *SecretChannelSnapshot) Save(ctx context.Context, data []byte) error {
	secrets := s.Client.CoreV1().Secrets(s.Namespace)
	sec, err := secrets.Get(ctx, FallbackChannelsSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: FallbackChannelsSecret, Namespace: s.Namespace, Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
			}},
			Data: map[string][]byte{fallbackChannelsKey: data},
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data[fallbackChannelsKey] = data
	_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
	return err
}

// SetChannelSnapshotStore loads the stored channel list (when this
// process has not read one from Postgres yet) and saves every later
// change to it.
func (d *Dispatcher) SetChannelSnapshotStore(ctx context.Context, s ChannelSnapshotStore) {
	if d == nil || s == nil {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, snapshotSaveTimeout)
	raw, err := s.Load(lctx)
	cancel()
	var loaded []db.Notification
	if err == nil && len(raw) > 0 && json.Unmarshal(raw, &loaded) == nil {
		d.lastGoodMu.Lock()
		if d.lastGood == nil {
			d.lastGood = loaded
		}
		d.lastGoodMu.Unlock()
	} else if err != nil && d.logger != nil {
		d.logger.Warn("notify: load fallback channel snapshot", "err", err)
	}
	d.snapMu.Lock()
	d.snapStore = s
	if len(raw) > 0 {
		d.snapSaved = sha256.Sum256(raw)
	}
	d.snapMu.Unlock()
	// A list read before the store was attached (boot order) is saved now.
	d.saveSnapshot()
}

// saveSnapshot writes the current fallback list to the store when it
// differs from what is there. It reads the list itself, so concurrent
// calls can't leave an older list saved last.
func (d *Dispatcher) saveSnapshot() {
	d.snapMu.Lock()
	defer d.snapMu.Unlock()
	n := d.lastGoodChannels()
	if d.snapStore == nil || n == nil {
		return
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return
	}
	sum := sha256.Sum256(raw)
	if sum == d.snapSaved {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotSaveTimeout)
	defer cancel()
	if err := d.snapStore.Save(ctx, raw); err != nil {
		if d.logger != nil {
			d.logger.Warn("notify: save fallback channel snapshot", "err", err)
		}
		return
	}
	d.snapSaved = sum
}
