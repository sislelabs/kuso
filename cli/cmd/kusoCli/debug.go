package kusoCli

/*
Copyright © 2023 NAME HERE <EMAIL ADDRESS>
*/

import (
	_ "embed"
	"os"
	"os/exec"
	"runtime"

	"github.com/i582/cfmt/cmd/cfmt"
	"github.com/spf13/cobra"
)

// debugCmd represents the debug command
var debugCmd = &cobra.Command{
	Use:     "debug",
	Aliases: []string{"dbg"},
	Short:   "Print debug informations",
	Long: `This command will print debug informations like:
	- Kuso CLI version
	- OS/Arch
	- Kubernetes version
	- Kusop operator version
	- Kusop operator namespace
	- Kubernetes metrics server version
	- Kubernetes cert-manager version`,
	Run: func(cmd *cobra.Command, args []string) {
		_, _ = cfmt.Println("{{Kuso CLI}}::bold")
		printCLIVersion()
		printOsArch()
		_, _ = cfmt.Println("\n{{Kubernetes}}::bold")
		printKubernetesVersion()
		_, _ = cfmt.Println("{{Kuso Operator}}::bold")
		checkKusoOperator()
		_, _ = cfmt.Println("{{\nKuso UI}}::bold")
		checkKusoUI()
		_, _ = cfmt.Println("{{\nCert Manager}}::bold")
		checkCertManager()
	},
}

func init() {
	rootCmd.AddCommand(debugCmd)
}

func printCLIVersion() {
	_, _ = cfmt.Println("kusoCLIVersion: ", kusoCliVersion)
}

func printOsArch() {
	_, _ = cfmt.Println("OS: ", runtime.GOOS)
	_, _ = cfmt.Println("Arch: ", runtime.GOARCH)
	_, _ = cfmt.Println("goVersion: ", runtime.Version())
}

func printKubernetesVersion() {
	hasKubectl := checkBinary("kubectl")
	if !hasKubectl {
		promptWarning("kubectl is not installed. Installer won't be able to install kuso. Please install kubectl and try again.")
		os.Exit(1)
	}
	version, _ := exec.Command("kubectl", "version", "-o", "yaml").Output()
	_, _ = cfmt.Println(string(version))
}

func checkKusoOperator() {
	cmdOut, _ := exec.Command("kubectl", "get", "deployments.apps", "-n", "kuso-operator-system").Output()
	_, _ = cfmt.Print(string(cmdOut))

	_, _ = cfmt.Println("{{\nKuso Operator Image}}::bold")
	cmdOut, _ = exec.Command("kubectl", "get", "deployment", "kuso-operator-controller-manager", "-o=jsonpath={$.spec.template.spec.containers[:1].image}", "-n", "kuso-operator-system").Output()
	_, _ = cfmt.Print(string(cmdOut))
	_, _ = cfmt.Println("")
}

func checkKusoUI() {
	cmdOut, _ := exec.Command("kubectl", "get", "deployments.apps", "-n", "kuso").Output()
	_, _ = cfmt.Print(string(cmdOut))

	_, _ = cfmt.Println("{{\nKuso UI Ingress}}::bold")
	cmdOut, _ = exec.Command("kubectl", "get", "ingress", "-n", "kuso").Output()
	_, _ = cfmt.Print(string(cmdOut))

	_, _ = cfmt.Println("{{\nKuso UI Secrets}}::bold")
	cmdOut, _ = exec.Command("kubectl", "get", "secrets", "-n", "kuso").Output()
	_, _ = cfmt.Print(string(cmdOut))

	_, _ = cfmt.Println("{{\nKuso UI Image}}::bold")
	cmdOut, _ = exec.Command("kubectl", "get", "deployment", "kuso", "-o=jsonpath={$.spec.template.spec.containers[:1].image}", "-n", "kuso").Output()
	_, _ = cfmt.Print(string(cmdOut))
	_, _ = cfmt.Println("")
}

func checkCertManager() {
	cmdOut, _ := exec.Command("kubectl", "get", "deployments.apps", "-n", "cert-manager").Output()
	_, _ = cfmt.Print(string(cmdOut))

	_, _ = cfmt.Println("{{\nCert Manager Cluster Issuers}}::bold")
	cmdOut, _ = exec.Command("kubectl", "get", "clusterissuers.cert-manager.io").Output()
	_, _ = cfmt.Print(string(cmdOut))
}
