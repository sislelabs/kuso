package logs

import "encoding/json"

// jsonMarshal is a thin alias used by stream_test.go to verify the wire
// shape of Frame without polluting stream.go with a test-only export.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
