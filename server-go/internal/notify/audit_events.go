package notify

import (
	"fmt"
	"strconv"
	"time"
)

// AuditChainHead is one audit hash chain's head at checkpoint time.
type AuditChainHead struct {
	Chain    string
	Seq      int64
	Hash     string // hex
	FloorSeq int64
}

// AuditCheckpoint carries the audit chain heads off-box. Instance-wide,
// so only unscoped channels get it and it stays out of the bell feed.
// Extra holds the machine-readable copy for webhook consumers.
func AuditCheckpoint(heads []AuditChainHead, at time.Time) Event {
	e := Event{
		Type:        EventAuditCheckpoint,
		Audience:    AudienceUnscoped,
		Severity:    "info",
		Timestamp:   at,
		Title:       "Audit log checkpoint",
		Description: "Keep this message. If the audit log is later edited, `kuso audit verify --chain <chain> --seq <seq> --hash <hash>` with these values reports it.",
		Extra:       map[string]string{"checkpointAt": at.UTC().Format(time.RFC3339)},
	}
	for _, h := range heads {
		e.Fields = append(e.Fields, EventField{
			Name:  h.Chain,
			Value: fmt.Sprintf("seq %d (oldest kept %d)\n`%s`", h.Seq, h.FloorSeq, h.Hash),
		})
		e.Extra[h.Chain+".seq"] = strconv.FormatInt(h.Seq, 10)
		e.Extra[h.Chain+".hash"] = h.Hash
		e.Extra[h.Chain+".floorSeq"] = strconv.FormatInt(h.FloorSeq, 10)
	}
	e.Body = e.Description
	return e
}
