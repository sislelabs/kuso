// db_row.go — `kuso db row insert|update|delete`: the DB browser's
// single-row editor. Every write is one row, addressed by the table's
// full primary key for update/delete; the server refuses anything that
// would touch more or fewer than one row. Postgres only (ClickHouse
// addons are read-only in the data browser).

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// parseCellFlags turns repeated col=value flags plus --null col flags
// into the server's cell map. Values go over the wire as strings and
// Postgres casts them to the column type, so "42", "true" and
// "2026-01-01" all work. Splits on the first '='.
func parseCellFlags(flag string, kvs, nulls []string) (map[string]kusoApi.SQLCell, error) {
	out := map[string]kusoApi.SQLCell{}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("--%s %q: want COLUMN=VALUE", flag, kv)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("--%s: column %q given twice", flag, k)
		}
		out[k] = kusoApi.SQLCell{Value: v}
	}
	for _, k := range nulls {
		k = strings.TrimSpace(k)
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("column %q is both --%s and --null", k, flag)
		}
		out[k] = kusoApi.SQLCell{IsNull: true}
	}
	return out, nil
}

type dbRowFlags struct {
	schema, table, database, output string
	set, nulls, where               []string
	yes                             bool
}

func (f *dbRowFlags) register(c *cobra.Command, withSet, withWhere bool) {
	c.Flags().StringVar(&f.table, "table", "", "table name (required)")
	c.Flags().StringVar(&f.schema, "schema", "public", "table schema")
	c.Flags().StringVar(&f.database, "database", "", "logical database to target (default: the addon's own)")
	if withSet {
		c.Flags().StringArrayVar(&f.set, "set", nil, "column value COLUMN=VALUE (repeatable)")
		c.Flags().StringArrayVar(&f.nulls, "null", nil, "set this column to NULL (repeatable)")
	}
	if withWhere {
		c.Flags().StringArrayVar(&f.where, "where", nil, "primary-key column PK=VALUE (repeat for composite keys; must cover the whole key)")
	}
}

func renderRowWrite(output string, body []byte) error {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if output == "json" {
		return jsonOut(data)
	}
	if row, ok := data["row"]; ok {
		if renderSQLTable(os.Stdout, map[string]any{"columns": data["columns"], "rows": []any{row}}) {
			return nil
		}
	}
	return jsonOut(data)
}

func newDBRowInsertCmd() *cobra.Command {
	f := &dbRowFlags{}
	c := &cobra.Command{
		Use:   "insert <project> <addon>",
		Short: "Insert one row into a table",
		Long: `Insert one row and print it as stored (defaults and generated ids
filled in). Columns you omit take their default. Values are sent as text
and cast by Postgres to the column type. Requires the project admin role.`,
		Example: `  kuso db row insert shop db --table todo --set title='buy milk'
  kuso db row insert shop db --table todo --set title=x --set done=true -o json
  kuso db row insert shop db --schema app --table notes --set body=hi --null author_id`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if f.table == "" {
				return fmt.Errorf("--table is required")
			}
			values, err := parseCellFlags("set", f.set, f.nulls)
			if err != nil {
				return err
			}
			if len(values) == 0 {
				return fmt.Errorf("pass at least one --set COLUMN=VALUE or --null COLUMN")
			}
			resp, err := api.SQLInsertRow(args[0], args[1], f.database, kusoApi.SQLRowWrite{
				Schema: f.schema, Table: f.table, Values: values,
			})
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("insert row: %w", err)
			}
			return renderRowWrite(f.output, resp.Body())
		},
	}
	f.register(c, true, false)
	c.Flags().StringVarP(&f.output, "output", "o", "table", "output format [table, json]")
	return c
}

func newDBRowUpdateCmd() *cobra.Command {
	f := &dbRowFlags{}
	c := &cobra.Command{
		Use:   "update <project> <addon>",
		Short: "Update one row, addressed by its full primary key",
		Long: `Update the columns given with --set/--null on exactly one row. --where
must name every primary-key column; tables without a primary key can't be
edited here (use 'kuso db sql' or psql). The server rolls back and errors
if the key matches no row (404) or more than one (409).`,
		Example: `  kuso db row update shop db --table todo --where id=1 --set done=true
  kuso db row update shop db --table memberships --where org_id=4 --where user_id=9 --set role=admin`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if f.table == "" {
				return fmt.Errorf("--table is required")
			}
			set, err := parseCellFlags("set", f.set, f.nulls)
			if err != nil {
				return err
			}
			if len(set) == 0 {
				return fmt.Errorf("pass at least one --set COLUMN=VALUE or --null COLUMN")
			}
			pk, err := parseCellFlags("where", f.where, nil)
			if err != nil {
				return err
			}
			if len(pk) == 0 {
				return fmt.Errorf("--where PK=VALUE is required (the full primary key)")
			}
			resp, err := api.SQLUpdateRow(args[0], args[1], f.database, kusoApi.SQLRowWrite{
				Schema: f.schema, Table: f.table, Set: set, PK: pk,
			})
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("update row: %w", err)
			}
			return renderRowWrite(f.output, resp.Body())
		},
	}
	f.register(c, true, true)
	c.Flags().StringVarP(&f.output, "output", "o", "table", "output format [table, json]")
	return c
}

func newDBRowDeleteCmd() *cobra.Command {
	f := &dbRowFlags{}
	c := &cobra.Command{
		Use:     "delete <project> <addon>",
		Aliases: []string{"rm"},
		Short:   "Delete one row, addressed by its full primary key",
		Long: `Delete exactly one row. --where must name every primary-key column.
The row is gone once this returns and is not recoverable short of a
backup restore. The server rolls back if the key matches no row (404) or
more than one (409). Prompts unless --yes.`,
		Example: `  kuso db row delete shop db --table todo --where id=7
  kuso db row delete shop db --table todo --where id=7 --yes`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if f.table == "" {
				return fmt.Errorf("--table is required")
			}
			pk, err := parseCellFlags("where", f.where, nil)
			if err != nil {
				return err
			}
			if len(pk) == 0 {
				return fmt.Errorf("--where PK=VALUE is required (the full primary key)")
			}
			if err := confirmDestructive(f.yes, fmt.Sprintf("Delete the row %s from %s.%s in %s/%s? Not recoverable without a backup.",
				strings.Join(f.where, ","), f.schema, f.table, args[0], args[1])); err != nil {
				return err
			}
			resp, err := api.SQLDeleteRow(args[0], args[1], f.database, kusoApi.SQLRowWrite{
				Schema: f.schema, Table: f.table, PK: pk,
			})
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("delete row: %w", err)
			}
			fmt.Printf("deleted 1 row from %s.%s (%s)\n", f.schema, f.table, strings.Join(f.where, ","))
			return nil
		},
	}
	f.register(c, false, true)
	c.Flags().BoolVarP(&f.yes, "yes", "y", false, "skip the confirmation prompt")
	return c
}

var dbRowCmd = &cobra.Command{
	Use:   "row",
	Short: "Insert, update or delete single rows (DB browser editor)",
}

func init() {
	dbRowCmd.AddCommand(newDBRowInsertCmd(), newDBRowUpdateCmd(), newDBRowDeleteCmd())
	dbCmd.AddCommand(dbRowCmd)
}
