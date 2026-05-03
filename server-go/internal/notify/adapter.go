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
}

// Emit2 is the EventEmitter-compatible signature kept on Dispatcher.
// We name it differently from the typed Emit so each call site can
// pick the level of typing it wants. Both feed the same dispatch
// queue.
func (d *Dispatcher) EmitEnvelope(env EmitEnvelope) {
	d.Emit(Event{
		Type:     EventType(env.Type),
		Title:    env.Title,
		Body:     env.Body,
		Project:  env.Project,
		Service:  env.Service,
		URL:      env.URL,
		Severity: env.Severity,
		Extra:    env.Extra,
	})
}
