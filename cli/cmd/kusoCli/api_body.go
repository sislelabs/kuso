// api_body.go — request-body + path/method assembly for `kuso api`.
// Split out from api.go so the coercion rules are unit-testable without
// a cobra command or a live server.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// normalizeAPIPath makes a user-supplied path target the API surface.
// "projects" -> "/api/projects"; an existing /api prefix is kept.
func normalizeAPIPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasPrefix(p, "/api/") && p != "/api" {
		p = "/api" + p
	}
	return p
}

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

func validateMethod(m string) (string, error) {
	up := strings.ToUpper(m)
	if !allowedMethods[up] {
		return "", fmt.Errorf("unsupported method %q (use GET|POST|PUT|PATCH|DELETE)", m)
	}
	return up, nil
}

// coerceFieldValue applies gh-style coercion to a -f value: an integer
// becomes a JSON number, true/false a bool, null a null, @file the raw
// file contents (as a string), anything else a string.
func coerceFieldValue(v string) (any, error) {
	switch {
	case v == "true":
		return true, nil
	case v == "false":
		return false, nil
	case v == "null":
		return nil, nil
	case strings.HasPrefix(v, "@"):
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return nil, fmt.Errorf("read field file %q: %w", v[1:], err)
		}
		return string(b), nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n, nil
	}
	return v, nil
}

// buildBody assembles the request body. --data (raw JSON, or @file) is
// mutually exclusive with -f/-F. -f coerces values; -F keeps them as
// strings. Returns nil when there is no body.
func buildBody(dataFlag string, fFields, capFFields []string) ([]byte, error) {
	hasFields := len(fFields) > 0 || len(capFFields) > 0
	if dataFlag != "" && hasFields {
		return nil, fmt.Errorf("--data/-d cannot be combined with -f/-F")
	}
	if dataFlag != "" {
		if strings.HasPrefix(dataFlag, "@") {
			b, err := os.ReadFile(dataFlag[1:])
			if err != nil {
				return nil, fmt.Errorf("read --data file %q: %w", dataFlag[1:], err)
			}
			return b, nil
		}
		return []byte(dataFlag), nil
	}
	if !hasFields {
		return nil, nil
	}
	obj := map[string]any{}
	for _, f := range fFields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("-f %q must be key=value", f)
		}
		cv, err := coerceFieldValue(v)
		if err != nil {
			return nil, err
		}
		obj[k] = cv
	}
	for _, f := range capFFields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("-F %q must be key=value", f)
		}
		obj[k] = v // always string
	}
	return json.Marshal(obj)
}
