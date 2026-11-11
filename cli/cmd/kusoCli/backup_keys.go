package kusoCli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
	"github.com/spf13/cobra"
)

// `kuso backup keys` — get the per-addon backup encryption keys out of the
// cluster (and back in after a rebuild). Without them every encrypted dump
// in the bucket is unreadable once the cluster is gone.

var (
	backupKeysFile      string
	backupKeysRecipient string
	backupKeysIdentity  string
)

// writeSecretFile writes data 0600, refusing to replace an existing file.
func writeSecretFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; refusing to overwrite it", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

var backupKeysCmd = &cobra.Command{
	Use:   "keys",
	Short: "Export/import the backup encryption keys (admin)",
	Long: `Each database addon encrypts its dumps with a key that lives only in a
Secret in this cluster. If the cluster is lost, those dumps are unreadable
unless the keys were exported. Export them after creating addons and keep
the file off-cluster (password manager, offline storage). The file is
equivalent to the keys themselves; seal it to a recipient key
(kuso backup keys keygen) to store it next to the backups.`,
}

var backupKeysExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Write every backup encryption key to a file (0600)",
	Args:  cobra.NoArgs,
	Example: `  kuso backup keys export --file kuso-keys.json
  kuso backup keys export --recipient "$(cat kuso-keys.pub)" --file kuso-keys.sealed`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		path := "/api/admin/backup/keys"
		if backupKeysRecipient != "" {
			if _, err := apiv1.ParseBackupKeyRecipient(backupKeysRecipient); err != nil {
				return fmt.Errorf("--recipient: %w", err)
			}
			path += "?recipient=" + url.QueryEscape(strings.TrimSpace(backupKeysRecipient))
		}
		resp, err := api.RawGet(path)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("export backup keys: %w", err)
		}
		var out apiv1.BackupKeyExportResponse
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		var data []byte
		file := backupKeysFile
		stamp := time.Now().UTC().Format("20060102T150405Z")
		if backupKeysRecipient != "" {
			if data, err = base64.StdEncoding.DecodeString(out.Sealed); err != nil || len(data) == 0 {
				return fmt.Errorf("server returned no sealed bundle (older server without --recipient support?)")
			}
			if file == "" {
				file = "kuso-backup-keys-" + stamp + ".sealed"
			}
		} else {
			if out.Bundle == nil {
				return fmt.Errorf("server returned no bundle")
			}
			if data, err = json.MarshalIndent(out.Bundle, "", "  "); err != nil {
				return err
			}
			if file == "" {
				file = "kuso-backup-keys-" + stamp + ".json"
			}
		}
		if err := writeSecretFile(file, data); err != nil {
			return err
		}
		fmt.Printf("wrote %d backup key(s) to %s\n", out.Count, file)
		if backupKeysRecipient == "" {
			fmt.Fprintln(os.Stderr, "WARNING: this file holds the keys in plaintext; anyone with it and bucket access can read every backup. Store it off-cluster and offline.")
		}
		for _, w := range out.Warnings {
			fmt.Fprintln(os.Stderr, "warning: "+w)
		}
		return nil
	},
}

var backupKeysImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Recreate missing backup key Secrets from an export (disaster recovery)",
	Long: `Run on a rebuilt cluster after the projects exist (their namespaces must)
and before re-creating the addons, so each addon picks up its old key
instead of minting a new one. Secrets that already hold a different key are
reported as conflicts and never overwritten.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		if data, err = maybeOpenSealed(data); err != nil {
			return err
		}
		resp, err := api.RawPost("/api/admin/backup/keys", data, "application/json")
		if err != nil {
			return err
		}
		var out apiv1.BackupKeyImportResponse
		if resp.StatusCode() == 409 || resp.StatusCode() < 300 {
			if err := json.Unmarshal(resp.Body(), &out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
		} else if err := checkRespErr(resp, nil); err != nil {
			return fmt.Errorf("import backup keys: %w", err)
		}
		failed := 0
		for _, r := range out.Results {
			line := fmt.Sprintf("%-10s %s/%s", r.Status, r.Namespace, r.Secret)
			if r.Detail != "" {
				line += ": " + r.Detail
			}
			fmt.Println(line)
			if r.Status == "conflict" || r.Status == "error" {
				failed++
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d key(s) not imported", failed)
		}
		return nil
	},
}

// maybeOpenSealed decrypts a sealed export with --identity; a plain JSON
// bundle passes through.
func maybeOpenSealed(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte("KUSOKEYS")) {
		return data, nil
	}
	if backupKeysIdentity == "" {
		return nil, fmt.Errorf("this export is sealed; pass --identity <private key file> from 'kuso backup keys keygen'")
	}
	raw, err := os.ReadFile(backupKeysIdentity)
	if err != nil {
		return nil, err
	}
	id, err := apiv1.ParseBackupKeyIdentity(string(raw))
	if err != nil {
		return nil, fmt.Errorf("--identity: %w", err)
	}
	return apiv1.OpenBackupKeys(id, data)
}

var backupKeysKeygenCmd = &cobra.Command{
	Use:   "keygen",
	Short: "Create a recipient key pair for sealed exports",
	Long: `Writes the private key to --identity (0600) and prints the public key.
Pass the public key to 'kuso backup keys export --recipient'. Keep the
private key off-cluster: it is what opens a sealed export.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if backupKeysIdentity == "" {
			return fmt.Errorf("--identity <file> is required")
		}
		priv, pub, err := apiv1.GenerateBackupKeyIdentity()
		if err != nil {
			return err
		}
		if err := writeSecretFile(backupKeysIdentity, []byte(priv+"\n")); err != nil {
			return err
		}
		fmt.Println(pub)
		return nil
	},
}

var backupKeysDecryptCmd = &cobra.Command{
	Use:   "decrypt <sealed-file>",
	Short: "Open a sealed export into a plaintext bundle (0600)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if backupKeysIdentity == "" {
			return fmt.Errorf("--identity <file> is required")
		}
		if backupKeysFile == "" {
			return fmt.Errorf("--file <path> is required")
		}
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(data, []byte("KUSOKEYS")) {
			return fmt.Errorf("%s is not a sealed export", args[0])
		}
		pt, err := maybeOpenSealed(data)
		if err != nil {
			return err
		}
		return writeSecretFile(backupKeysFile, pt)
	},
}

func init() {
	backupCmd.AddCommand(backupKeysCmd)
	backupKeysCmd.AddCommand(backupKeysExportCmd, backupKeysImportCmd, backupKeysKeygenCmd, backupKeysDecryptCmd)
	// --file, not -o: on every other command -o selects the output format.
	backupKeysExportCmd.Flags().StringVar(&backupKeysFile, "file", "", "destination file (default: kuso-backup-keys-<timestamp>.json|.sealed)")
	backupKeysExportCmd.Flags().StringVar(&backupKeysRecipient, "recipient", "", "public key from 'kuso backup keys keygen'; seals the export to it")
	backupKeysImportCmd.Flags().StringVar(&backupKeysIdentity, "identity", "", "private key file, for a sealed export")
	backupKeysKeygenCmd.Flags().StringVar(&backupKeysIdentity, "identity", "", "where to write the private key (0600)")
	backupKeysDecryptCmd.Flags().StringVar(&backupKeysIdentity, "identity", "", "private key file from keygen")
	backupKeysDecryptCmd.Flags().StringVar(&backupKeysFile, "file", "", "where to write the plaintext bundle (0600)")
}
