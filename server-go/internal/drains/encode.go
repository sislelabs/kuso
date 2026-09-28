package drains

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Encode renders a batch in the drain type's wire format.
func Encode(t Type, lines []Line) ([]byte, error) {
	switch t {
	case TypeHTTP:
		return json.Marshal(httpBatch(lines))
	case TypeOTLP:
		return json.Marshal(otlpBatch(lines))
	case TypeLoki:
		return json.Marshal(lokiBatch(lines))
	}
	return nil, fmt.Errorf("%w: unknown type %q", ErrInvalid, t)
}

// httpLine is the generic JSON drain record. stream is always
// "combined": the kubelet log API interleaves stdout and stderr without
// marking which is which.
type httpLine struct {
	Ts      time.Time `json:"ts"`
	Project string    `json:"project"`
	Service string    `json:"service"`
	Env     string    `json:"env"`
	Pod     string    `json:"pod"`
	Stream  string    `json:"stream"`
	Line    string    `json:"line"`
}

func httpBatch(lines []Line) []httpLine {
	out := make([]httpLine, len(lines))
	for i, l := range lines {
		out[i] = httpLine{Ts: l.Ts.UTC(), Project: l.Project, Service: l.Service, Env: l.Env, Pod: l.Pod, Stream: "combined", Line: l.Line}
	}
	return out
}

type streamKey struct{ project, service, env string }

// groupByStream buckets lines per (project, service, env), preserving
// first-seen order so output is deterministic.
func groupByStream(lines []Line) ([]streamKey, map[streamKey][]Line) {
	var order []streamKey
	groups := map[streamKey][]Line{}
	for _, l := range lines {
		k := streamKey{l.Project, l.Service, l.Env}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], l)
	}
	return order, groups
}

// OTLP/HTTP JSON (protobuf JSON mapping: int64 nanos as strings).

type otlpAnyValue struct {
	StringValue string `json:"stringValue"`
}

type otlpKV struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpRecord struct {
	TimeUnixNano         string       `json:"timeUnixNano"`
	ObservedTimeUnixNano string       `json:"observedTimeUnixNano"`
	Body                 otlpAnyValue `json:"body"`
	Attributes           []otlpKV     `json:"attributes"`
}

type otlpScopeLogs struct {
	Scope struct {
		Name string `json:"name"`
	} `json:"scope"`
	LogRecords []otlpRecord `json:"logRecords"`
}

type otlpResourceLogs struct {
	Resource struct {
		Attributes []otlpKV `json:"attributes"`
	} `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpExport struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

func kv(k, v string) otlpKV { return otlpKV{Key: k, Value: otlpAnyValue{StringValue: v}} }

func nanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func otlpBatch(lines []Line) otlpExport {
	order, groups := groupByStream(lines)
	out := otlpExport{ResourceLogs: make([]otlpResourceLogs, 0, len(order))}
	for _, k := range order {
		ls := groups[k]
		var rl otlpResourceLogs
		// deployment.environment is the env kind (production/preview);
		// kuso.env is the concrete env CR name.
		rl.Resource.Attributes = []otlpKV{
			kv("service.name", k.project+"/"+k.service),
			kv("kuso.project", k.project),
			kv("kuso.service", k.service),
			kv("kuso.env", k.env),
			kv("deployment.environment", ls[0].EnvKind),
		}
		sl := otlpScopeLogs{LogRecords: make([]otlpRecord, len(ls))}
		sl.Scope.Name = "kuso"
		for i, l := range ls {
			sl.LogRecords[i] = otlpRecord{
				TimeUnixNano:         nanos(l.Ts),
				ObservedTimeUnixNano: nanos(l.Observed),
				Body:                 otlpAnyValue{StringValue: l.Line},
				Attributes:           []otlpKV{kv("k8s.pod.name", l.Pod)},
			}
		}
		rl.ScopeLogs = []otlpScopeLogs{sl}
		out.ResourceLogs = append(out.ResourceLogs, rl)
	}
	return out
}

// Loki push API JSON. Labels stay low-cardinality (no pod): Loki's
// index cost scales with distinct label sets, and pods churn per deploy.

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

type lokiPush struct {
	Streams []lokiStream `json:"streams"`
}

func lokiBatch(lines []Line) lokiPush {
	order, groups := groupByStream(lines)
	out := lokiPush{Streams: make([]lokiStream, 0, len(order))}
	for _, k := range order {
		ls := groups[k]
		s := lokiStream{
			Stream: map[string]string{"project": k.project, "service": k.service, "env": k.env},
			Values: make([][2]string, len(ls)),
		}
		for i, l := range ls {
			s.Values[i] = [2]string{nanos(l.Ts), l.Line}
		}
		out.Streams = append(out.Streams, s)
	}
	return out
}
