package kusoCli

// Instance is one kuso server the CLI knows about. Stored in
// ~/.kuso/kuso.yaml under "instances:" so a developer can switch
// between (e.g.) staging + production with `kuso remote select`.
//
// Name is the map key in the config file; ApiUrl is what we hit. The
// IacBaseDir is where `kuso init` writes its kuso.yml — kept on the
// instance entry so different instances can target different folders
// in the same repo without colliding.
type Instance struct {
	Name       string `json:"-" yaml:"-"`
	ApiUrl     string `yaml:"apiurl"`
	IacBaseDir string `yaml:"iacBaseDir,omitempty"`
	ConfigPath string `json:"-" yaml:"-"`
}
