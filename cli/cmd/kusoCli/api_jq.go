// api_jq.go — optional --jq filtering for `kuso api`, backed by gojq
// (pure-Go, no cgo). Isolated in its own file so the dependency is easy
// to drop.

package kusoCli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

// applyJQ runs a jq expression over a JSON body and returns each result
// as a newline-terminated JSON value.
func applyJQ(body []byte, expr string) ([]byte, error) {
	var input any
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, fmt.Errorf("response is not JSON, cannot apply --jq: %w", err)
	}
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid jq expression: %w", err)
	}
	var out bytes.Buffer
	iter := query.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			return nil, fmt.Errorf("jq: %w", err)
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out.Write(enc)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
