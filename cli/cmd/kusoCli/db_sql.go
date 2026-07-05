// `kuso db` SQL read/query subcommands — a thin CLI over the addon SQL
// browser surface. Read-only: list tables, introspect columns, page rows,
// and run an arbitrary read-only SELECT. The destructive grid-editor row
// mutations (insert/update/delete) are deliberately NOT exposed here.
//
//   kuso db tables  <project> <addon> [-o json]
//   kuso db columns <project> <addon> --table t [--schema s] [-o json]
//   kuso db rows    <project> <addon> --table t [--schema s] [--limit N] [-o json]
//   kuso db sql     <project> <addon> "SELECT ..." [--limit N] [-o json]
//
// Every subcommand is admin-gated server-side (same secret-bearing-read
// boundary as env values); you get a 403 otherwise. Registered onto the
// existing dbCmd (defined in db.go, same package) via init().

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"kuso/pkg/kusoApi"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	sqlSchema string
	sqlTable  string
	sqlLimit  int
)

var dbTablesCmd = &cobra.Command{
	Use:   "tables <project> <addon>",
	Short: "List the addon database's user tables",
	Long: `List the base tables in an addon's database (pg_catalog and
information_schema are filtered out). Requires the project admin role.`,
	Args: cobra.ExactArgs(2),
	Example: `  kuso db tables scubatony scubatony-db
  kuso db tables scubatony scubatony-db -o json | jq -r '.[].name'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.SQLTables(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list tables: %w", err)
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"SCHEMA", "TABLE"})
			for _, it := range items {
				t.Append([]string{asString(it["schema"]), asString(it["name"])})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var dbColumnsCmd = &cobra.Command{
	Use:   "columns <project> <addon> --table <table> [--schema <schema>]",
	Short: "Show a table's columns, types, and primary key",
	Long: `Introspect one table's columns (name, type, nullability, default,
enum values) plus its primary key. --schema defaults to "public".
Requires the project admin role.`,
	Args: cobra.ExactArgs(2),
	Example: `  kuso db columns scubatony scubatony-db --table users
  kuso db columns scubatony scubatony-db --table users --schema public -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if sqlTable == "" {
			return fmt.Errorf("--table is required")
		}
		resp, err := api.SQLColumns(args[0], args[1], sqlSchema, sqlTable)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list columns: %w", err)
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(data)
		case "table", "":
			pk := map[string]bool{}
			for _, c := range asAnySlice(data["primaryKey"]) {
				pk[asString(c)] = true
			}
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"COLUMN", "TYPE", "NULLABLE", "PK", "DEFAULT"})
			for _, cv := range asAnySlice(data["columns"]) {
				c, ok := cv.(map[string]any)
				if !ok {
					continue
				}
				name := asString(c["name"])
				pkMark := ""
				if pk[name] {
					pkMark = "yes"
				}
				t.Append([]string{
					name,
					sqlTypeText(c),
					boolText(c["nullable"]),
					pkMark,
					asString(c["default"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var dbRowsCmd = &cobra.Command{
	Use:   "rows <project> <addon> --table <table> [--schema <schema>] [--limit N]",
	Short: "Page rows from a single table (JSON)",
	Long: `Fetch a page of rows from one table. --schema defaults to "public",
--limit defaults to the server's page size (100, max 1000). Output is
JSON only — rows can be wide, so a table view is a poor fit. Requires the
project admin role.`,
	Args: cobra.ExactArgs(2),
	Example: `  kuso db rows scubatony scubatony-db --table users
  kuso db rows scubatony scubatony-db --table users --limit 10 | jq '.rows'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if sqlTable == "" {
			return fmt.Errorf("--table is required")
		}
		resp, err := api.SQLRows(args[0], args[1], sqlSchema, sqlTable, sqlLimit, 0)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("fetch rows: %w", err)
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return jsonOut(data)
	},
}

var dbSQLCmd = &cobra.Command{
	Use:   "sql <project> <addon> <query> [--limit N]",
	Short: "Run a read-only SELECT against an addon database",
	Long: `Run an arbitrary read-only SELECT against an addon's database. The
query executes inside a read-only transaction server-side; writes and a
handful of high-blast-radius builtins (pg_read_file, dblink, COPY, …) are
rejected. --limit caps the returned rows (default 100, max 1000).

Requires the project admin role.`,
	Args: cobra.ExactArgs(3),
	Example: `  kuso db sql scubatony scubatony-db "SELECT 1 AS ok"
  kuso db sql scubatony scubatony-db "SELECT id, email FROM users" --limit 20
  kuso db sql scubatony scubatony-db "SELECT count(*) FROM users" -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.SQLQuery(args[0], args[1], kusoApi.SQLQueryRequest{
			Query: args[2],
			Limit: sqlLimit,
		})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("run query: %w", err)
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(data)
		case "table", "":
			cols := asAnySlice(data["columns"])
			// Render as a table when the response is the expected
			// columns+rows shape; anything else falls back to JSON so we
			// never silently drop a differently-shaped response.
			if _, ok := data["rows"]; !ok || len(cols) == 0 {
				return jsonOut(data)
			}
			header := make([]string, len(cols))
			for i, c := range cols {
				header[i] = asString(c)
			}
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader(header)
			for _, rv := range asAnySlice(data["rows"]) {
				cells := asAnySlice(rv)
				row := make([]string, len(header))
				for i := range header {
					if i < len(cells) {
						row[i] = asString(cells[i])
					}
				}
				t.Append(row)
			}
			t.Render()
			if b, ok := data["truncated"].(bool); ok && b {
				fmt.Fprintln(os.Stderr, "[kuso] result truncated — pass --limit to fetch more")
			}
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

// sqlTypeText renders a column's display type, preferring the enum type
// name (udtName) for USER-DEFINED columns so an enum shows as its type
// rather than a bare "USER-DEFINED".
func sqlTypeText(c map[string]any) string {
	dt := asString(c["dataType"])
	if dt == "USER-DEFINED" {
		if udt := asString(c["udtName"]); udt != "" {
			return udt
		}
	}
	return dt
}

// asAnySlice coerces a decoded JSON value to []any, returning nil for
// any other shape (absent key, null, wrong type) so callers can range
// over it safely.
func asAnySlice(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

func init() {
	dbColumnsCmd.Flags().StringVar(&sqlSchema, "schema", "public", "table schema")
	dbColumnsCmd.Flags().StringVar(&sqlTable, "table", "", "table name (required)")

	dbRowsCmd.Flags().StringVar(&sqlSchema, "schema", "public", "table schema")
	dbRowsCmd.Flags().StringVar(&sqlTable, "table", "", "table name (required)")
	dbRowsCmd.Flags().IntVar(&sqlLimit, "limit", 0, "max rows to return (0 = server default 100, max 1000)")

	dbSQLCmd.Flags().IntVar(&sqlLimit, "limit", 0, "max rows to return (0 = server default 100, max 1000)")

	// -o output format for the SQL read subcommands. dbCmd doesn't define a
	// persistent --output (it's a connection command group), so scope the
	// flag to each SQL subcommand that renders structured output.
	for _, c := range []*cobra.Command{dbTablesCmd, dbColumnsCmd, dbRowsCmd, dbSQLCmd} {
		c.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	}

	dbCmd.AddCommand(dbTablesCmd)
	dbCmd.AddCommand(dbColumnsCmd)
	dbCmd.AddCommand(dbRowsCmd)
	dbCmd.AddCommand(dbSQLCmd)
}
