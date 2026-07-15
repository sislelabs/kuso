package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso ssh-key` — manage the SSH key library that backs the SSH-driven
// "add node" flow (`kuso node validate/join --ssh-key-id <id>`). All
// admin-gated server-side.
//
//   kuso ssh-key list
//   kuso ssh-key add <name> --generate
//   kuso ssh-key add <name> --public-key-file pub --private-key-file priv
//   kuso ssh-key rm <id>

var sshKeyCmd = &cobra.Command{
	Use:     "ssh-key",
	Aliases: []string{"ssh-keys", "sshkey"},
	Short:   "Manage the SSH key library (used by node validate/join).",
}

var sshKeyListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List stored SSH keys (private bytes are never shown).",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListSSHKeys()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var keys []kusoApi.SSHKey
		if err := json.Unmarshal(resp.Body(), &keys); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(keys) == 0 {
			fmt.Println("No SSH keys.")
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"ID", "Name", "Fingerprint", "Created"})
		for _, k := range keys {
			tw.Append([]string{k.ID, k.Name, k.Fingerprint, k.CreatedAt})
		}
		tw.Render()
		return nil
	},
}

var (
	sshKeyGenerate bool
	sshKeyPubFile  string
	sshKeyPrivFile string
)

var sshKeyAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add an SSH key (server-generated with --generate, or imported).",
	Long: `Add an SSH key to the library. Either let kuso generate a fresh
ed25519 keypair (--generate; the private half stays server-side), or
import an existing pair with --public-key-file and --private-key-file.
The created key's public half + fingerprint are printed so you can drop
it into a remote authorized_keys.`,
	Example: `  kuso ssh-key add my-fleet-key --generate
  kuso ssh-key add imported --public-key-file ~/.ssh/id_ed25519.pub --private-key-file ~/.ssh/id_ed25519`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.CreateSSHKeyRequest{Name: args[0]}
		if sshKeyGenerate {
			if sshKeyPubFile != "" || sshKeyPrivFile != "" {
				return fmt.Errorf("--generate cannot be combined with --public-key-file / --private-key-file")
			}
			req.Generate = true
		} else {
			if sshKeyPubFile == "" || sshKeyPrivFile == "" {
				return fmt.Errorf("pass --generate, or both --public-key-file and --private-key-file")
			}
			pub, err := os.ReadFile(sshKeyPubFile)
			if err != nil {
				return fmt.Errorf("--public-key-file: %w", err)
			}
			priv, err := os.ReadFile(sshKeyPrivFile)
			if err != nil {
				return fmt.Errorf("--private-key-file: %w", err)
			}
			req.PublicKey = string(pub)
			req.PrivateKey = string(priv)
		}
		resp, err := api.CreateSSHKey(req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var key kusoApi.SSHKey
		if err := json.Unmarshal(resp.Body(), &key); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		fmt.Printf("SSH key %q added (id %s, fingerprint %s).\n", key.Name, key.ID, key.Fingerprint)
		if key.PublicKey != "" {
			fmt.Println("\nPublic key (add to the remote host's authorized_keys):")
			fmt.Println(key.PublicKey)
		}
		fmt.Printf("\nUse it: kuso node join --ssh-key-id %s ...\n", key.ID)
		return nil
	},
}

var sshKeyRmCmd = &cobra.Command{
	Use:     "rm <id>",
	Aliases: []string{"remove", "delete"},
	Short:   "Delete a stored SSH key by id.",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeleteSSHKey(args[0])
		if err != nil {
			return err
		}
		switch resp.StatusCode() {
		case 204:
			fmt.Printf("Deleted SSH key %s.\n", args[0])
			return nil
		case 404:
			return fmt.Errorf("SSH key %s not found", args[0])
		default:
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
	},
}

func init() {
	sshKeyListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	sshKeyCmd.AddCommand(sshKeyListCmd)

	sshKeyAddCmd.Flags().BoolVar(&sshKeyGenerate, "generate", false, "let kuso generate a fresh ed25519 keypair (private stays server-side)")
	sshKeyAddCmd.Flags().StringVar(&sshKeyPubFile, "public-key-file", "", "path to the public key to import")
	sshKeyAddCmd.Flags().StringVar(&sshKeyPrivFile, "private-key-file", "", "path to the private key to import")
	sshKeyAddCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	sshKeyCmd.AddCommand(sshKeyAddCmd)

	sshKeyCmd.AddCommand(sshKeyRmCmd)

	rootCmd.AddCommand(sshKeyCmd)
}
