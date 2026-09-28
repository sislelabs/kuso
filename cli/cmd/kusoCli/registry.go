package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage private container-registry credentials (runtime=image pulls)",
	Long: `Store credentials for private registries (GHCR, ECR, Docker Hub, …) so
runtime=image services can pull private images. Each credential is a
per-project Secret; attach it to a service with

  kuso project service set <project> <service> --image-pull-secret <registry>

Passwords are write-only: kuso never prints or returns them.`,
}

var registryLoginUsername string

// Flag vars for `service set --watch-paths / --image-pull-secret`
// (registered in project.go alongside the other service-set flags).
var (
	serviceSetWatchPaths      string
	serviceSetImagePullSecret string
)

var registryLoginCmd = &cobra.Command{
	Use:   "login <project> <registry>",
	Short: "Store (or replace) a registry credential; the password is read from stdin",
	Example: `  echo "$GHCR_TOKEN" | kuso registry login shop ghcr.io --username octo --password-stdin
  aws ecr get-login-password | kuso registry login shop 123456789.dkr.ecr.eu-west-1.amazonaws.com --username AWS --password-stdin`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req, err := buildRegistryLogin(args[1], registryLoginUsername, os.Stdin)
		if err != nil {
			return err
		}
		resp, err := api.RegistryLogin(args[0], req)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("registry login: %w", err)
		}
		var cred kusoApi.RegistryCredential
		if err := json.Unmarshal(resp.Body(), &cred); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		fmt.Printf("stored %s credential for %s in project %s (secret %s)\n", cred.Username, cred.Registry, args[0], cred.SecretName)
		fmt.Printf("attach it: kuso project service set %s <service> --image-pull-secret %s\n", args[0], cred.Registry)
		return nil
	},
}

var registryListCmd = &cobra.Command{
	Use:     "list <project>",
	Aliases: []string{"ls"},
	Short:   "List a project's registry credentials (passwords are never shown)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListRegistryCredentials(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var body struct {
			Credentials []kusoApi.RegistryCredential `json:"credentials"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(body.Credentials) == 0 {
			fmt.Println("No registry credentials.")
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"Registry", "Username", "Secret", "Updated"})
		for _, c := range body.Credentials {
			tw.Append([]string{c.Registry, c.Username, c.SecretName, c.UpdatedAt})
		}
		tw.Render()
		return nil
	},
}

var registryLogoutYes bool

var registryLogoutCmd = &cobra.Command{
	Use:   "logout <project> <registry>",
	Short: "Delete a registry credential",
	Long: `Delete a project's credential for a registry. The stored password is not
recoverable. The server refuses while any service still references the
credential (clear it with --image-pull-secret "" first), because the next
pod restart would fail to pull.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(registryLogoutYes,
			fmt.Sprintf("Delete the %s credential for project %s? The password is not recoverable.", args[1], args[0])); err != nil {
			return err
		}
		resp, err := api.RegistryLogout(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("registry logout: %w", err)
		}
		fmt.Printf("removed %s credential from project %s\n", args[1], args[0])
		return nil
	},
}

// buildRegistryLogin assembles the login request, reading the password
// from stdin so it never lands in shell history or the process list.
func buildRegistryLogin(registry, username string, stdin io.Reader) (kusoApi.RegistryLoginRequest, error) {
	if strings.TrimSpace(username) == "" {
		return kusoApi.RegistryLoginRequest{}, fmt.Errorf("--username is required")
	}
	data, err := io.ReadAll(io.LimitReader(stdin, 64<<10))
	if err != nil {
		return kusoApi.RegistryLoginRequest{}, fmt.Errorf("read password from stdin: %w", err)
	}
	pw := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(pw) == "" {
		return kusoApi.RegistryLoginRequest{}, fmt.Errorf("no password on stdin (pipe it in and pass --password-stdin)")
	}
	return kusoApi.RegistryLoginRequest{Registry: registry, Username: strings.TrimSpace(username), Password: pw}, nil
}

// parseWatchPathsFlag splits --watch-paths on commas/newlines. An empty
// flag yields a non-nil empty list, which the server reads as "clear".
func parseWatchPathsFlag(raw string) []string {
	out := []string{}
	for _, p := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// imagePullSecretPatch builds the image patch for --image-pull-secret.
// The server replaces spec.image wholesale, so the current repository
// and tag are round-tripped from the fetched service.
func imagePullSecretPatch(currentService []byte, ref string) (*kusoApi.PatchImageRequest, error) {
	var cur struct {
		Spec struct {
			Image *struct {
				Repository string `json:"repository"`
				Tag        string `json:"tag"`
			} `json:"image"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(currentService, &cur); err != nil {
		return nil, fmt.Errorf("decode current service: %w", err)
	}
	if cur.Spec.Image == nil || cur.Spec.Image.Repository == "" {
		return nil, fmt.Errorf("--image-pull-secret applies to runtime=image services; this service has no image")
	}
	ref = strings.TrimSpace(ref)
	return &kusoApi.PatchImageRequest{Repository: cur.Spec.Image.Repository, Tag: cur.Spec.Image.Tag, PullSecret: &ref}, nil
}

func init() {
	registryLoginCmd.Flags().StringVarP(&registryLoginUsername, "username", "u", "", "registry username (for ECR: AWS)")
	// Accepted for docker-login muscle memory; stdin is the only password source.
	registryLoginCmd.Flags().Bool("password-stdin", true, "read the password from stdin (the only supported source)")
	registryCmd.AddCommand(registryLoginCmd)

	registryListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	registryCmd.AddCommand(registryListCmd)

	registryLogoutCmd.Flags().BoolVarP(&registryLogoutYes, "yes", "y", false, "skip the confirmation prompt")
	registryCmd.AddCommand(registryLogoutCmd)

	rootCmd.AddCommand(registryCmd)
}
