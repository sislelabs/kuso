module kuso

go 1.25.0

require (
	github.com/go-resty/resty/v2 v2.16.5
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674
	github.com/itchyny/gojq v0.12.19
	github.com/mattn/go-shellwords v1.0.12
	github.com/olekukonko/tablewriter v0.0.5
	github.com/sislelabs/kuso/api/apiv1 v0.0.0
	github.com/sislelabs/kuso/compose v0.0.0
	github.com/sislelabs/kuso/coolify v0.0.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/viper v1.20.1
	gopkg.in/yaml.v3 v3.0.1
)

// Local-repo modules. Replace directives pin to in-tree copies so
// tagged CLI releases don't require publishing each module
// separately.
replace github.com/sislelabs/kuso/api/apiv1 => ../api/apiv1

replace github.com/sislelabs/kuso/coolify => ../coolify

replace github.com/sislelabs/kuso/compose => ../compose

require (
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.3.0 // indirect
	github.com/compose-spec/compose-go/v2 v2.4.7 // indirect
	github.com/distribution/reference v0.5.0 // indirect
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.2.1 // indirect
	github.com/itchyny/timefmt-go v0.1.8 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sirupsen/logrus v1.9.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/xeipuuv/gojsonpointer v0.0.0-20180127040702-4e3ac2762d5f // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/xeipuuv/gojsonschema v1.2.0 // indirect
	golang.org/x/exp v0.0.0-20240112132812-db7319d0e0e3 // indirect
	golang.org/x/sync v0.20.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

require (
	github.com/AlecAivazis/survey/v2 v2.3.7
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fatih/color v1.18.0
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/mgutz/ansi v0.0.0-20200706080929-d51e80ef957d // indirect
	github.com/pelletier/go-toml/v2 v2.2.3 // indirect
	github.com/sagikazarmark/locafero v0.8.0 // indirect
	github.com/sourcegraph/conc v0.3.0 // indirect
	github.com/spf13/afero v1.14.0 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/term v0.42.0
	golang.org/x/text v0.36.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)
