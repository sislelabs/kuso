package kusoCli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// A few commands historically bound -o to the output FILE, while ~100
// others use -o for the output FORMAT, so `-o json` wrote a file named
// "json". The file flag is long-only now; -o survives as a hidden alias
// that refuses format-looking values and warns otherwise.

func bindLegacyOutFileShorthand(cmd *cobra.Command, legacy *string) {
	cmd.Flags().StringVarP(legacy, "output", "o", "", "deprecated alias for the output file flag")
	_ = cmd.Flags().MarkHidden("output")
}

// resolveLegacyOutFile merges the hidden -o value into the real file
// flag. fileFlag is the flag users should type instead (e.g. "--out").
func resolveLegacyOutFile(current, legacy, fileFlag string) (string, error) {
	if legacy == "" {
		return current, nil
	}
	if err := rejectFormatAsPath(legacy, fileFlag); err != nil {
		return "", err
	}
	if current != "" && current != legacy {
		return "", fmt.Errorf("-o is a deprecated alias for %s; pass only %s", fileFlag, fileFlag)
	}
	fmt.Fprintf(os.Stderr, "warning: -o is deprecated here (elsewhere it selects the output format); use %s %s\n", fileFlag, legacy)
	return legacy, nil
}

func rejectFormatAsPath(value, fileFlag string) error {
	switch strings.ToLower(value) {
	case "json", "yaml", "yml", "table", "text", "wide":
		return fmt.Errorf("-o %s: this command has no output-format flag and -o no longer names the output file; use %s <path>, or omit it to write to stdout", value, fileFlag)
	}
	return nil
}
