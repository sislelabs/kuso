// api.go — `kuso api <METHOD> <path>`, a gh-api-style escape hatch that
// hits any /api endpoint with the CLI's configured auth. Keeps the CLI
// small: an endpoint is reachable here the moment it ships, before it
// earns a dedicated typed command.

package kusoCli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/spf13/cobra"
)

var (
	apiData    string
	apiFields  []string
	apiCapF    []string
	apiHeaders []string
	apiInclude bool
	apiMethodX string
	apiJQ      string
)

// renderAPIResponse pretty-prints JSON bodies (indent 2), passes other
// bodies through raw, and — when include is set — writes a status line
// and sorted headers first. Always newline-terminates.
func renderAPIResponse(out io.Writer, body []byte, include bool, status int, header http.Header) error {
	if include {
		fmt.Fprintf(out, "HTTP %d\n", status)
		keys := make([]string, 0, len(header))
		for k := range header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range header[k] {
				fmt.Fprintf(out, "%s: %s\n", k, v)
			}
		}
		fmt.Fprintln(out)
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil && pretty.Len() > 0 {
		pretty.WriteByte('\n')
		_, err := out.Write(pretty.Bytes())
		return err
	}
	_, err := fmt.Fprintln(out, string(body))
	return err
}

var apiCmd = &cobra.Command{
	Use:   "api <METHOD> <path>",
	Short: "Call any kuso API endpoint directly (gh-api style).",
	Long: `Send an authenticated request to any /api endpoint using the
credentials from 'kuso login'. Useful for endpoints without a dedicated
command, scripting, and debugging.

The path may omit the leading slash and the /api prefix:
"projects" is treated as "/api/projects".`,
	Example: `  kuso api GET /api/projects
  kuso api GET projects
  kuso api POST /api/projects/acme/services/web/builds -f branch=main
  kuso api DELETE /api/projects/acme/grants/12
  kuso api POST /api/x --data '{"a":1}'
  kuso api GET projects -i
  kuso api GET projects --jq '.[].name'`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// METHOD path  OR  path (with -X METHOD, else defaults to GET)
		var methodArg, pathArg string
		if len(args) == 2 {
			methodArg, pathArg = args[0], args[1]
		} else if apiMethodX != "" {
			methodArg, pathArg = apiMethodX, args[0]
		} else {
			methodArg, pathArg = "GET", args[0]
		}
		method, err := validateMethod(methodArg)
		if err != nil {
			return err
		}
		path := normalizeAPIPath(pathArg)

		// resty is built with AllowGetMethodPayload=false, so a body on a
		// GET is silently dropped — the request goes out bodiless and the
		// user never learns their -d/-f/-F was ignored. Reject it up front
		// with a clear error rather than send a misleading request. (GET
		// with a body is non-idiomatic anyway; use POST/PUT/PATCH.)
		if method == "GET" && (apiData != "" || len(apiFields) > 0 || len(apiCapF) > 0) {
			return fmt.Errorf("GET requests can't carry a body — drop -d/-f/-F, or use a method that takes one (POST/PUT/PATCH)")
		}

		body, err := buildBody(apiData, apiFields, apiCapF)
		if err != nil {
			return err
		}
		headers := map[string]string{}
		for _, h := range apiHeaders {
			k, v, ok := cutHeader(h)
			if !ok {
				return fmt.Errorf("-H %q must be Key:Value", h)
			}
			headers[k] = v
		}

		resp, err := api.Raw(method, path, body, headers)
		if err != nil {
			return err
		}

		out := resp.Body()
		if apiJQ != "" {
			filtered, jerr := applyJQ(out, apiJQ)
			if jerr != nil {
				return jerr
			}
			out = filtered
		}
		if rerr := renderAPIResponse(cmd.OutOrStdout(), out, apiInclude, resp.StatusCode(), resp.Header()); rerr != nil {
			return rerr
		}
		if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode())
		}
		return nil
	},
}

// cutHeader splits "Key: Value" (optional space after colon).
func cutHeader(h string) (string, string, bool) {
	for i := 0; i < len(h); i++ {
		if h[i] == ':' {
			k := h[:i]
			v := h[i+1:]
			if len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			return k, v, k != ""
		}
	}
	return "", "", false
}

func init() {
	apiCmd.Flags().StringVarP(&apiData, "data", "d", "", "raw JSON request body (or @file.json)")
	apiCmd.Flags().StringArrayVarP(&apiFields, "field", "f", nil, "typed field key=value (repeatable; coerces numbers/bools/null; @file reads a value)")
	apiCmd.Flags().StringArrayVarP(&apiCapF, "raw-field", "F", nil, "string field key=value (repeatable; no coercion)")
	apiCmd.Flags().StringArrayVarP(&apiHeaders, "header", "H", nil, "extra request header Key:Value (repeatable)")
	apiCmd.Flags().BoolVarP(&apiInclude, "include", "i", false, "print response status + headers before the body")
	apiCmd.Flags().StringVarP(&apiMethodX, "method", "X", "", "HTTP method (alternative to the positional METHOD)")
	apiCmd.Flags().StringVar(&apiJQ, "jq", "", "filter the JSON response through a jq expression")
	rootCmd.AddCommand(apiCmd)
}
