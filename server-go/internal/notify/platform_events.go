package notify

import (
	"fmt"
	"time"
)

// PlatformUnhealthy: a kuso platform workload (Traefik, buildkitd, the
// registry, the operator, ...) has been crashlooping or without a ready
// pod past the watcher's grace period.
func PlatformUnhealthy(component, namespace, reason string, since time.Time) Event {
	e := Event{
		Type:        EventPlatformUnhealthy,
		Severity:    "error",
		Title:       "✗ Platform · " + component + " unhealthy",
		Description: reason,
		Body:        reason,
		Fields: []EventField{
			{Name: "Component", Value: component, Inline: true},
			{Name: "Namespace", Value: namespace, Inline: true},
		},
		Extra: map[string]string{"component": component, "namespace": namespace},
	}
	if tok := TimeToken(since); tok != "" {
		e.Fields = append(e.Fields, EventField{Name: "Since", Value: tok, Inline: true})
	}
	return e
}

// PlatformRecovered closes a PlatformUnhealthy, IngressDown or
// AlertEngineDegraded episode. component names what recovered.
func PlatformRecovered(component string, downFor time.Duration) Event {
	desc := component + " is healthy again"
	if downFor > 0 {
		desc += fmt.Sprintf(" after %s", downFor.Round(time.Second))
	}
	return Event{
		Type:        EventPlatformRecovered,
		Severity:    "info",
		Title:       "✓ Platform · " + component + " recovered",
		Description: desc,
		Body:        desc,
		DurationMs:  downFor.Milliseconds(),
		Extra:       map[string]string{"component": component},
	}
}

// IngressDown: the public ingress path stopped working. source says
// which signal saw it ("probe through Traefik", "traffic stopped").
func IngressDown(source, detail string) Event {
	return Event{
		Type:        EventIngressDown,
		Severity:    "error",
		Title:       "✗ Ingress down · " + source,
		Description: detail,
		Body:        detail,
		Extra:       map[string]string{"source": source},
	}
}

// AlertEngineDegraded: the alert rule engine can't reach Prometheus, so
// the HTTP error-rate / latency / traffic rules are blind.
func AlertEngineDegraded(failures int, lastErr string) Event {
	desc := fmt.Sprintf("Prometheus queries failed %d checks in a row; HTTP alert rules are not being evaluated. Last error: %s", failures, lastErr)
	return Event{
		Type:        EventAlertEngineDown,
		Severity:    "error",
		Title:       "✗ Alert engine degraded · Prometheus unreachable",
		Description: desc,
		Body:        desc,
	}
}
