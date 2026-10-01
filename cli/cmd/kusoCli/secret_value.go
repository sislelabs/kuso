package kusoCli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Secret values passed as arguments land in shell history and `ps`.
// These commands also take the value from --from-file <path> or from
// stdin when the value is "-".

var (
	secretSetFromFile         string
	sharedSecretSetFromFile   string
	instanceSecretSetFromFile string
)

var secretStdin io.Reader = os.Stdin

// resolveSecretValue picks the value from --from-file, stdin ("-"), or
// the literal. A file is used byte-for-byte; stdin drops one trailing
// newline (what `echo`/`pbpaste` add).
func resolveSecretValue(literal string, haveLiteral bool, fromFile string) (string, error) {
	if fromFile != "" {
		if haveLiteral {
			return "", fmt.Errorf("pass the value either inline or with --from-file, not both")
		}
		b, err := os.ReadFile(fromFile)
		if err != nil {
			return "", fmt.Errorf("read --from-file: %w", err)
		}
		return string(b), nil
	}
	if !haveLiteral {
		return "", fmt.Errorf("missing value: pass it inline, as - to read stdin, or with --from-file <path>")
	}
	if literal != "-" {
		return literal, nil
	}
	if stdinIsTTYFn() {
		fmt.Fprint(os.Stderr, "value (end with Ctrl-D): ")
	}
	b, err := io.ReadAll(secretStdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	v := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(v, "\r"), nil
}

// splitSecretKV parses "KEY=VALUE" or a bare "KEY" (value from
// --from-file or stdin) and resolves the value.
func splitSecretKV(arg, fromFile string) (key, value string, err error) {
	key, val, haveVal := strings.Cut(arg, "=")
	if key == "" {
		return "", "", fmt.Errorf("argument must be KEY=VALUE (or KEY with --from-file, or KEY=- for stdin)")
	}
	value, err = resolveSecretValue(val, haveVal, fromFile)
	return key, value, err
}
