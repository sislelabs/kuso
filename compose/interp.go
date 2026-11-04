package compose

import (
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// resolveService returns a copy of svc with interpolate applied to the
// values the converter carries over (image, command, entrypoint, build
// args, environment). Whatever stays unresolved is flagged and recorded
// for the --apply gate. The caller's project is not mutated.
func resolveService(svc types.ServiceConfig, rep *Report) types.ServiceConfig {
	note := func(what, val string, vars []string) {
		if len(vars) == 0 {
			return
		}
		rep.flag(svc.Name, "%s %q references ${%s} with no default — compose would take it from the shell, the import can't; it deploys as the literal text. Set the value in kuso", what, val, strings.Join(vars, "}, ${"))
		rep.unresolvedVar(svc.Name, what+" (${"+strings.Join(vars, "}, ${")+"})")
	}
	resolveList := func(what string, in []string) []string {
		if len(in) == 0 {
			return in
		}
		out := make([]string, len(in))
		for i, s := range in {
			var vars []string
			out[i], vars = interpolate(s)
			note(what, s, vars)
		}
		return out
	}

	var vars []string
	orig := svc.Image
	svc.Image, vars = interpolate(svc.Image)
	note("image", orig, vars)
	svc.Command = resolveList("command", svc.Command)
	svc.Entrypoint = resolveList("entrypoint", svc.Entrypoint)
	if svc.Build != nil && len(svc.Build.Args) > 0 {
		b := *svc.Build
		b.Args = types.MappingWithEquals{}
		for k, v := range svc.Build.Args {
			if v != nil {
				r, vs := interpolate(*v)
				note("build arg "+k, *v, vs)
				v = &r
			}
			b.Args[k] = v
		}
		svc.Build = &b
	}
	if len(svc.Environment) > 0 {
		env := types.MappingWithEquals{}
		for k, v := range svc.Environment {
			if v != nil {
				r, vs := interpolate(*v)
				note("env "+k, *v, vs)
				v = &r
			}
			env[k] = v
		}
		svc.Environment = env
	}
	return svc
}

// interpolate applies compose variable substitution to s with every
// variable treated as unset: the import never reads the importer's
// shell (see Parse), so the only values that can be resolved are the
// defaults the file itself carries.
//
//	$$                  → $
//	${V:-def} ${V-def}  → def (itself interpolated)
//	${V:+alt} ${V+alt}  → "" (V is unset)
//	$V ${V} ${V:?err}   → kept verbatim, V reported as unresolved
func interpolate(s string) (string, []string) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var out strings.Builder
	var unresolved []string
	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) {
			out.WriteByte(s[i])
			i++
			continue
		}
		switch next := s[i+1]; {
		case next == '$':
			out.WriteByte('$')
			i += 2
		case next == '{':
			end := matchBrace(s, i+2)
			if end < 0 {
				// Unterminated: nothing compose would accept; keep it.
				out.WriteString(s[i:])
				unresolved = append(unresolved, s[i:])
				return out.String(), unresolved
			}
			body := s[i+2 : end]
			name, op, arg := splitExpr(body)
			switch op {
			case ":-", "-":
				v, u := interpolate(arg)
				out.WriteString(v)
				unresolved = append(unresolved, u...)
			case ":+", "+":
			default:
				out.WriteString(s[i : end+1])
				unresolved = append(unresolved, name)
			}
			i = end + 1
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			out.WriteString(s[i:j])
			unresolved = append(unresolved, s[i+1:j])
			i = j
		default:
			out.WriteByte('$')
			i++
		}
	}
	return out.String(), unresolved
}

// matchBrace returns the index of the '}' closing a "${" whose body
// starts at from, honouring nested "${…}" in defaults; -1 if none.
func matchBrace(s string, from int) int {
	depth := 1
	for i := from; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitExpr splits a "${…}" body into the variable name, the operator
// (":-", "-", ":+", "+", ":?", "?" or "") and its argument.
func splitExpr(body string) (name, op, arg string) {
	j := 0
	for j < len(body) && isNameChar(body[j]) {
		j++
	}
	name, rest := body[:j], body[j:]
	for _, o := range []string{":-", ":+", ":?", "-", "+", "?"} {
		if strings.HasPrefix(rest, o) {
			return name, o, rest[len(o):]
		}
	}
	return name, "", rest
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }
