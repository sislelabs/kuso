package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SQL console audit rows are listed by GET /api/audit?project=X, which
// only needs the project viewer role, while the SQL console itself is
// admin-only. So the rows keep what was touched (op, table, columns, the
// query's shape) and a hash to match a known value against, never the
// values or query literals themselves (OPS-4).

// sqlWriteAuditMessage describes an insert/update/delete: the column names
// of key, and a hash of its values.
func sqlWriteAuditMessage(op, schema, table string, key map[string]cellValue) string {
	cols := make([]string, 0, len(key))
	for c := range key {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	raw, _ := json.Marshal(key) // map keys marshal sorted, so the hash is stable
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%s %s.%s columns=[%s] values=sha256:%s",
		op, schema, table, strings.Join(cols, ","), hex.EncodeToString(sum[:])[:16])
}

// sqlQueryAuditMessage is the query with its literals replaced by ?,
// truncated to 4 KiB, prefixed with a hash of the full original text so
// "did this exact query run twice" stays answerable.
func sqlQueryAuditMessage(query string) string {
	sum := sha256.Sum256([]byte(query))
	q := redactSQLLiterals(query)
	if len(q) > 4096 {
		q = q[:4096] + "…[truncated]"
	}
	return fmt.Sprintf("[sha256:%s] %s", hex.EncodeToString(sum[:])[:16], q)
}

// redactSQLLiterals replaces string literals ('…', E'…', $tag$…$tag$),
// numbers and comment bodies with ?. Double-quoted identifiers are kept.
// It is a scanner, not a parser: an unterminated literal redacts to the
// end of the text, so it errs toward hiding.
func redactSQLLiterals(q string) string {
	var b strings.Builder
	n := len(q)
	isIdent := func(c byte) bool {
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
	}
	for i := 0; i < n; {
		c := q[i]
		switch {
		case c == '-' && i+1 < n && q[i+1] == '-':
			end := strings.IndexByte(q[i:], '\n')
			if end < 0 {
				end = n - i
			}
			b.WriteString("-- ?")
			i += end
		case c == '/' && i+1 < n && q[i+1] == '*':
			end := strings.Index(q[i+2:], "*/")
			b.WriteString("/* ? */")
			if end < 0 {
				i = n
			} else {
				i += 2 + end + 2
			}
		case c == '"':
			j := i + 1
			for j < n {
				if q[j] == '"' {
					if j+1 < n && q[j+1] == '"' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, n)
			b.WriteString(q[i:end])
			i = end
		case c == '\'' || (c == 'E' || c == 'e') && i+1 < n && q[i+1] == '\'' && (i == 0 || !isIdent(q[i-1])):
			backslash := c != '\''
			j := i + 1
			if backslash {
				j++
			}
			for j < n {
				if backslash && q[j] == '\\' {
					j += 2
					continue
				}
				if q[j] == '\'' {
					if j+1 < n && q[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			b.WriteByte('?')
			i = min(j+1, n)
		case c == '$' && (i == 0 || !isIdent(q[i-1])):
			k := i + 1
			for k < n && q[k] != '$' && (isIdent(q[k]) && !(k == i+1 && q[k] >= '0' && q[k] <= '9')) {
				k++
			}
			if k < n && q[k] == '$' {
				tag := q[i : k+1]
				end := strings.Index(q[k+1:], tag)
				b.WriteByte('?')
				if end < 0 {
					i = n
				} else {
					i = k + 1 + end + len(tag)
				}
				continue
			}
			j := i + 1 // $1 placeholder or a lone $
			for j < n && q[j] >= '0' && q[j] <= '9' {
				j++
			}
			b.WriteString(q[i:j])
			i = j
		case c >= '0' && c <= '9' && (i == 0 || !isIdent(q[i-1])):
			j := i
			for j < n && (isIdent(q[j]) || q[j] == '.') {
				j++
			}
			b.WriteByte('?')
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
