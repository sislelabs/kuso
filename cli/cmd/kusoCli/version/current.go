package version

import (
	_ "embed"
	"fmt"
	"github.com/spf13/cobra"
	"net/http"
	"strings"
	"time"
)

var (
	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number of kusoCli",
		Long:  "Print the version number of kusoCli",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(GetVersionInfo())
		},
	}
	subLatestCmd = &cobra.Command{
		Use:   "latest",
		Short: "Print the latest version number of kusoCli",
		Long:  "Print the latest version number of kusoCli",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(GetLatestVersionInfo())
		},
	}
	subCmdCheck = &cobra.Command{
		Use:   "check",
		Short: "Check if the current version is the latest version of kusoCli",
		Long:  "Check if the current version is the latest version of kusoCli",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(GetVersionInfoWithLatestAndCheck())
		},
	}
	//subCmdUpgrade = &cobra.Command{
	//	Use:   "upgrade",
	//	Short: "Upgrade kusoCli to the latest version",
	//	Long:  "Upgrade kusoCli to the latest version",
	//	Run: func(cmd *cobra.Command, args []string) {
	//		syncCmd := CmdUpgradeCLIAndCheck()
	//		if err := syncCmd.Start(); err != nil {
	//			fmt.Println("Error: " + err.Error())
	//		}
	//	},
	//}
	//subCmdUpgradeCheck = &cobra.Command{
	//	Use:    "check",
	//	Hidden: true,
	//	RunE: func(cmd *cobra.Command, args []string) error {
	//		return UpgradeCLI()
	//	},
	//}
)

const gitModelUrl = "https://github.com/sislelabs/kuso"

//go:embed CLI_VERSION
var embeddedVersion string

// ldflagsVersion is set at build time via:
//
//	go build -ldflags="-X kuso/cli/cmd/kusoCli/version.ldflagsVersion=$(git describe --tags --always)"
//
// Falls back to the embedded CLI_VERSION file (kept in sync by
// hack/release.sh) so dev builds without ldflags still report something.
var ldflagsVersion string

func GetVersion() string {
	if v := strings.TrimSpace(ldflagsVersion); v != "" {
		return v
	}
	if v := strings.TrimSpace(embeddedVersion); v != "" {
		return v
	}
	return "dev"
}

func GetGitModelUrl() string {
	return gitModelUrl
}

func GetVersionInfo() string {
	return "Version: " + GetVersion() + "\n" + "Git repository: " + GetGitModelUrl()
}

func GetLatestVersionFromGit() string {
	netClient := &http.Client{
		Timeout: time.Second * 10,
	}

	response, err := netClient.Get(gitModelUrl + "/releases/latest")
	if err != nil {
		return "Error: " + err.Error()
	}

	if response.StatusCode != 200 {
		return "Error: " + response.Status
	}

	tag := strings.Split(response.Request.URL.Path, "/")

	return tag[len(tag)-1]
}

func GetLatestVersionInfo() string {
	return "Latest version: " + GetLatestVersionFromGit()
}

func GetVersionInfoWithLatestAndCheck() string {
	if GetVersion() == GetLatestVersionFromGit() {
		return GetVersionInfo() + "\n" + GetLatestVersionInfo() + "\n" + "You are using the latest version."
	} else {
		return GetVersionInfo() + "\n" + GetLatestVersionInfo() + "\n" + "You are using an outdated version.\n" + "Please upgrade your kusoCli to prevent any issues."
	}
}

//func UpgradeCLI() error {
//	if GetVersion() == GetLatestVersionFromGit() {
//		fmt.Println("You are using the latest version.")
//		return nil
//	} else {
//		netClient := &http.Client{
//			Timeout: time.Second * 10,
//		}
//
//		response, err := netClient.Get(gitModelUrl + "/releases/latest")
//		if err != nil {
//			return err
//		}
//
//		latestVersion := response.Status
//
//		if latestVersion == "200 OK" {
//			return fmt.Errorf("error: %s", latestVersion)
//		}
//
//		fileUrl := gitModelUrl + "/releases/download/" + latestVersion + "/kusoCli"
//
//		// Download the file
//		response, err = netClient.Get(fileUrl)
//		if err != nil {
//			return fmt.Errorf("error: %s", err)
//		}
//
//		// Save the file
//		writeFile, err := os.Create("kusoCli")
//		if err != nil {
//			return fmt.Errorf("error: %s", err)
//		}
//		defer func(writeFile *os.File) {
//			_ = writeFile.Close()
//		}(writeFile)
//
//		fileInfo, err := writeFile.Stat()
//		if err != nil {
//			return fmt.Errorf("error: %s", err)
//		}
//
//		fileMode := fileInfo.Mode()
//		if err := writeFile.Chmod(fileMode); err != nil {
//			return fmt.Errorf("error: %s", err)
//		}
//
//		currentExecutable, err := os.Executable()
//		if err != nil {
//			return fmt.Errorf("error: %s", err)
//		}
//
//		cmdCopy := "cp " + currentExecutable + " " + currentExecutable + ".old"
//		cmdRemove := "rm " + currentExecutable
//		cmdRename := "mv kusoCli " + currentExecutable
//		cmdUpgradeSpawner := cmdCopy + " && " + cmdRemove + " && " + cmdRename
//
//		spawner := os.Getenv("SHELL")
//		if spawner == "" {
//			spawner = "/bin/sh"
//		}
//
//		cmd := exec.Command(spawner, "-c", cmdUpgradeSpawner)
//		return cmd.Run()
//	}
//}
//
//func CmdUpgradeCLIAndCheck() *exec.Cmd {
//	cmd := exec.Command("kuso", "upgrade", "check")
//	return cmd
//}

func CliCommand() *cobra.Command {
	versionCmd.AddCommand(subLatestCmd)
	versionCmd.AddCommand(subCmdCheck)
	//subCmdUpgrade.AddCommand(subCmdUpgradeCheck)
	//versionCmd.AddCommand(subCmdUpgrade)
	return versionCmd
}
