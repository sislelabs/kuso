package kusoCli

import (
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// setUsageTemplate installs a small custom help template on a cobra
// command. The default template is fine but a touch dense; a few
// color hints make the section headers easier to scan in a terminal.
//
// We register the template func once on the root command and let
// children inherit. Calling this on every sub-command is harmless
// (idempotent) but unnecessary.
func setUsageTemplate(cmd *cobra.Command) {
	yellow := color.New(color.FgYellow).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()

	cobra.AddTemplateFunc("hdr", yellow)
	cobra.AddTemplateFunc("name", green)
	cobra.AddTemplateFunc("flags", cyan)

	cmd.SetUsageTemplate(usageTemplate)
}

// The template largely matches cobra's default but with hdr / name /
// flags pipes for color. Whitespace + ranges follow cobra conventions
// so the output looks familiar to anyone who's used a cobra-built CLI.
const usageTemplate = `{{hdr "Usage:"}}{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

{{hdr "Aliases:"}}
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

{{hdr "Examples:"}}
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

{{hdr "Available Commands:"}}
{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{name (rpad .Name .NamePadding)}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

{{hdr "Flags:"}}
{{flags (.LocalFlags.FlagUsages | trimTrailingWhitespaces)}}{{end}}{{if .HasAvailableInheritedFlags}}

{{hdr "Global Flags:"}}
{{flags (.InheritedFlags.FlagUsages | trimTrailingWhitespaces)}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`
