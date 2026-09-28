package main

import (
	"reflect"
	"testing"

	"kuso/server/internal/builds"
	"kuso/server/internal/notify"
)

// Build events cross three hand-written copies (builds.EventEnvelope →
// notify.EmitEnvelope → notify.Event). A field added to one and missed
// in the next is silently dropped from every build card, so pin the
// field-name parity here.
func TestNotifyEnvelopeFieldParity(t *testing.T) {
	names := func(v any) map[string]bool {
		out := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			out[rt.Field(i).Name] = true
		}
		return out
	}
	emit, event := names(notify.EmitEnvelope{}), names(notify.Event{})
	for f := range names(builds.EventEnvelope{}) {
		if !emit[f] {
			t.Errorf("builds.EventEnvelope.%s has no notify.EmitEnvelope counterpart", f)
		}
	}
	for f := range emit {
		if !event[f] {
			t.Errorf("notify.EmitEnvelope.%s has no notify.Event counterpart", f)
		}
	}
}
