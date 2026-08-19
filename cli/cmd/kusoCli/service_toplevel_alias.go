// service_toplevel_alias.go completes the top-level `kuso service …`
// alias for the `kuso project service …` subtree.
//
// The alias used to expose only 4 of the 10 subcommands (add, set,
// errors, pods) — so `kuso service stop` errored for a command that
// exists one level over. add/set keep their hand-rolled shells in
// project.go and errors/pods their re-minted constructors in
// service_diag.go; the remaining six are mirrored here via aliasOf.
//
// NOTE: this file's init() must run AFTER the originals' flags are
// registered (project.go, service_extra.go, service_stop.go) — Go runs
// package init()s in filename order, and "service_toplevel_alias.go"
// sorts after all of them. Keep the name late-sorting if you rename it.

package kusoCli

import "github.com/spf13/cobra"

// aliasOf mints a thin shell of src so the same subcommand can hang off
// a second parent (cobra sets cmd.parent on AddCommand, so one
// *cobra.Command cannot live under two parents). RunE, Args, and help
// are shared; the flags are the SAME pflag.Flag instances (their value
// bindings are package-level globals anyway), so both forms accept the
// identical flag set — including --yes on the guarded ones.
func aliasOf(src *cobra.Command) *cobra.Command {
	shell := &cobra.Command{
		Use:               src.Use,
		Aliases:           src.Aliases,
		Short:             src.Short,
		Long:              src.Long,
		Example:           src.Example,
		Args:              src.Args,
		RunE:              src.RunE,
		ValidArgsFunction: src.ValidArgsFunction,
	}
	shell.Flags().AddFlagSet(src.Flags())
	return shell
}

func init() {
	// serviceCmd is the top-level `kuso service` alias defined in
	// project.go; the originals live under projectServiceCmd.
	serviceCmd.AddCommand(
		aliasOf(serviceDeleteCmd),
		aliasOf(serviceRenameCmd),
		aliasOf(serviceStopCmd),
		aliasOf(serviceStartCmd),
		aliasOf(serviceWakeCmd),
		aliasOf(serviceDriftCmd),
	)
}
