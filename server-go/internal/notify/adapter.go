package notify

// EmitEnvelope accepts the lightweight envelope shape that
// dependency-light packages (e.g. internal/builds) emit. We translate
// to a full Event here so notify keeps its richer typed interface
// while consumers don't have to import this package.
//
// Field names mirror Event but as plain map[string]string for Extra
// — same encoding, no shared types.
type EmitEnvelope struct {
	Type     string
	Title    string
	Body     string
	Project  string
	Service  string
	URL      string
	Severity string
	Extra    map[string]string

	// Rich-card fields — same semantics as the matching Event fields.
	// Kept in lockstep with Event so the adapter is a pure forwarder.
	Description string
	LogTail     string
	DurationMs  int64
	Fields      []EnvelopeField
	Footer      string
}

// EnvelopeField mirrors EventField for callers that don't import
// notify. Same wire shape — see EventField for semantics.
type EnvelopeField struct {
	Name   string
	Value  string
	Inline bool
}

// Emit2 is the EventEmitter-compatible signature kept on Dispatcher.
// We name it differently from the typed Emit so each call site can
// pick the level of typing it wants. Both feed the same dispatch
// queue.
func (d *Dispatcher) EmitEnvelope(env EmitEnvelope) {
	fields := make([]EventField, 0, len(env.Fields))
	for _, f := range env.Fields {
		fields = append(fields, EventField{Name: f.Name, Value: f.Value, Inline: f.Inline})
	}
	d.Emit(Event{
		Type:        EventType(env.Type),
		Title:       env.Title,
		Body:        env.Body,
		Project:     env.Project,
		Service:     env.Service,
		URL:         env.URL,
		Severity:    env.Severity,
		Extra:       env.Extra,
		Description: env.Description,
		LogTail:     env.LogTail,
		DurationMs:  env.DurationMs,
		Fields:      fields,
		Footer:      env.Footer,
	})
}
