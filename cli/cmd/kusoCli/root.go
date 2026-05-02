package kusoCli

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"kuso/cmd/kusoCli/version"
	"os"
	"strings"

	"kuso/pkg/kusoApi"

	"github.com/AlecAivazis/survey/v2"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-resty/resty/v2"
	"github.com/i582/cfmt/cmd/cfmt"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	_ "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var (
	outputFormat        string
	force               bool
	repoSimpleList      []string
	api                 *kusoApi.KusoClient
	contextSimpleList   []string
	currentInstanceName string
	instanceList        map[string]Instance
	instanceNameList    []string
	currentInstance     Instance
	kusoCliVersion    string
	pipelineConfig      *viper.Viper
	credentialsConfig   *viper.Viper
)

var rootCmd = &cobra.Command{
	Use:   "kuso",
	Short: "Kuso is a platform as a service (PaaS) that enables developers to build, run, and operate applications on Kubernetes.",
	Long: `
	,--. ,--.        ,--.
	|  .'   /,--.,--.|  |-.  ,---. ,--.--. ,---.
	|  .   ' |  ||  || .-. '| .-. :|  .--'| .-. |
	|  |\   \'  ''  '| '-' |\   --.|  |   ' '-' '
	'--' '--' '----'  '---'  '----''--'    '---'
Documentation:
  https://www.kuso.sislelabs.com/docs
`,
	Example: `  kuso login --api https://kuso.example.com -u admin -p '<password>'
  kuso project create my-app --repo https://github.com/me/my-app
  kuso get projects`,
	Aliases: []string{"kbr"},
}

func Execute() {
	rootCmd.CompletionOptions.HiddenDefaultCmd = false

	rootCmd.AddCommand(version.CliCommand())

	SetUsageDefinition(rootCmd)

	loadCLIConfig()
	loadCredentials()
	api = new(kusoApi.KusoClient)
	api.Init(currentInstance.ApiUrl, credentialsConfig.GetString(currentInstanceName))

	for _, cmd := range rootCmd.Commands() {
		SetUsageDefinition(cmd)
	}

	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.CompletionOptions.HiddenDefaultCmd = true
}

func printCLI(table *tablewriter.Table, r *resty.Response) {
	if outputFormat == "json" {
		fmt.Println(r)
	} else {
		table.Render()
	}
}

func promptWarning(msg string) {
	_, _ = cfmt.Println("{{\n⚠️   " + msg + ".\n}}::yellow")
}

func promptLine(question, options, def string) string {
	if def != "" && force {
		_, _ = cfmt.Printf("\n{{?}}::green %s %s : {{%s}}::cyan\n", question, options, def)
		return def
	}
	reader := bufio.NewReader(os.Stdin)
	_, _ = cfmt.Printf("\n{{?}}::green|bold {{%s %s}}::bold {{%s}}::cyan : ", question, options, def)
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)
	if text == "" {
		text = def
	}
	return text
}

func selectFromList(question string, options []string, def string) string {
	_, _ = cfmt.Println("")
	if def != "" && force {
		_, _ = cfmt.Printf("\n{{?}}::green %s : {{%s}}::cyan\n", question, def)
		return def
	}
	prompt := &survey.Select{
		Message: question,
		Options: options,
	}
	askOneErr := survey.AskOne(prompt, &def)
	if askOneErr != nil {
		fmt.Println("Error while selecting:", askOneErr)
		return ""
	}
	return def
}

func confirmationLine(question, def string) bool {
	confirmation := promptLine(question, "[y,n]", def)
	if confirmation != "y" {
		_, _ = cfmt.Println("{{\n✗ Aborted\n}}::red")
		os.Exit(0)
		return false
	}
	return true
}


func getGitRemote() string {
	gitdir := getGitdir() + "/.git"
	fs := osfs.New(gitdir)
	s := filesystem.NewStorageWithOptions(fs, cache.NewObjectLRUDefault(), filesystem.Options{KeepDescriptors: true})
	r, err := git.Open(s, fs)
	if err == nil {
		remotes, _ := r.Remotes()
		return remotes[0].Config().URLs[0]
	}
	return ""
}

func getGitdir() string {
	wd, _ := os.Getwd()
	path := strings.Split(wd, "/")
	for i := len(path); i >= 0; i-- {
		subPath := strings.Join(path[:i], "/")
		fileInfo, err := os.Stat(subPath + "/.git")
		if err == nil && fileInfo.IsDir() {
			return subPath
		}
	}
	return ""
}

func getIACBaseDir() string {
	basePath := "."
	if currentInstance.IacBaseDir == "" {
		currentInstance.IacBaseDir = ".kuso"
		basePath += "/" + currentInstance.IacBaseDir
	}
	gitdir := getGitdir()
	if gitdir != "" {
		basePath = gitdir + "/" + currentInstance.IacBaseDir
	}
	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		_, _ = cfmt.Println("{{Creating directory}}::yellow " + basePath)
		mkDirAllErr := os.MkdirAll(basePath, 0755)
		if mkDirAllErr != nil {
			fmt.Println("Error while creating directory:", mkDirAllErr)
			return ""
		}
	}
	return basePath
}


func loadCLIConfig() {
	dir := getGitdir()
	repoConfig := viper.New()
	repoConfig.SetConfigName("kuso")
	repoConfig.SetConfigType("yaml")
	repoConfig.AddConfigPath(dir)
	repoConfig.ConfigFileUsed()
	errCred := repoConfig.ReadInConfig()

	viper.SetDefault("api.url", "http://default:2000")
	viper.SetConfigName("kuso")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("/etc/kuso/")
	viper.AddConfigPath("$HOME/.kuso/")
	err := viper.ReadInConfig()

	if err != nil && errCred != nil {
		var configFileNotFoundError viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFoundError) {
			fmt.Println("No config file found; using defaults")
		} else {
			fmt.Println("Error while loading config file:", err)
			return
		}
	}

	// Read instances directly from the YAML file because viper's "."
	// key delimiter splits dotted instance names like "kuso.sislelabs.com"
	// into nested maps, which UnmarshalKey then can't reassemble.
	if instanceList == nil {
		instanceList = map[string]Instance{}
	}
	if cfg := viper.ConfigFileUsed(); cfg != "" {
		raw, _ := os.ReadFile(cfg)
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err == nil {
			if insts, ok := doc["instances"].(map[string]any); ok {
				for k, v := range insts {
					if m, ok := v.(map[string]any); ok {
						instanceList[k] = Instance{
							Name:       k,
							ApiUrl:     asString(m["apiurl"]),
							IacBaseDir: asString(m["iacBaseDir"]),
							ConfigPath: cfg,
						}
					}
				}
			}
		}
	}

	var repoInstancesList map[string]Instance
	unmarshalKeyErr := repoConfig.UnmarshalKey("instances", &repoInstancesList)
	if unmarshalKeyErr != nil {
		fmt.Println("Error while unmarshalling instances:", unmarshalKeyErr)
		return
	}
	for instanceName, repoInstance := range repoInstancesList {
		repoInstance.Name = instanceName
		repoInstance.ConfigPath = repoConfig.ConfigFileUsed()
		instanceList[instanceName] = repoInstance
	}

	currentInstanceName = viper.GetString("currentInstance")
	for instanceName, instance := range instanceList {
		instance.Name = instanceName
		instanceNameList = append(instanceNameList, instanceName)
		if instanceName == currentInstanceName {
			currentInstance = instance
		}
	}
}

func loadCredentials() {
	// Use a non-dot key delimiter so instance names like
	// "kuso.sislelabs.com" don't get split into nested map keys by viper.
	credentialsConfig = viper.NewWithOptions(viper.KeyDelimiter("\x00"))
	credentialsConfig.SetConfigName("credentials")
	credentialsConfig.SetConfigType("yaml")
	credentialsConfig.AddConfigPath("/etc/kuso/")
	credentialsConfig.AddConfigPath("$HOME/.kuso/")
	err := credentialsConfig.ReadInConfig()
	if err != nil {
		fmt.Println("Error while loading credentialsConfig file:", err)
	}
}

func boolToEmoji(b bool) string {
	if b {
		return "✅"
	}
	return "❌"
}


func prettyPrintJson(data []byte) {
	var prettyJSON bytes.Buffer
	json.Indent(&prettyJSON, data, "", "\t")
	fmt.Println(prettyJSON.String())
}
