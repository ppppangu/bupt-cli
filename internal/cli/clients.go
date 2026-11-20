package cli

import (
	"io"
	"os"

	"bupt.local/cli/internal/connect"
	"github.com/spf13/cobra"
)

func (a *app) clientCommands() *cobra.Command {
	g := &cobra.Command{Use: "client", Short: "Register and revoke profile-scoped controller clients for CI"}
	list := a.simpleRPC("ls", "List registered controller clients", "client.list", 0, 0, nil)

	register := &cobra.Command{Use: "register NAME", Short: "Authorize a public key for selected profiles", Args: exactArgs(1)}
	register.Flags().String("public-key-file", "", "OpenSSH public key file")
	register.Flags().StringSlice("allow-profile", nil, "Allowed account profile (repeatable; defaults to selected profile)")
	register.Flags().Bool("read-only", false, "Allow inspection only, without SSH or mutations")
	register.Flags().Bool("owner", false, "Explicitly grant controller-owner authority")
	register.Flags().String("id", "", "Existing client ID to update; omit to create a new identity")
	register.RunE = func(cmd *cobra.Command, args []string) error {
		filename, _ := cmd.Flags().GetString("public-key-file")
		if filename == "" {
			return usage("--public-key-file is required")
		}
		f, e := os.Open(filename)
		if e != nil {
			return e
		}
		defer f.Close()
		data, e := io.ReadAll(io.LimitReader(f, 64*1024+1))
		if e != nil {
			return e
		}
		if len(data) > 64*1024 {
			return usage("public key file exceeds 64 KiB")
		}
		key, e := connect.CanonicalPublicKey(string(data))
		if e != nil {
			return e
		}
		profiles, _ := cmd.Flags().GetStringSlice("allow-profile")
		if len(profiles) == 0 {
			profiles = []string{a.cfg.Selected}
		}
		for _, profile := range profiles {
			if profile == "" {
				return usage("--allow-profile cannot be empty")
			}
		}
		readonly, _ := cmd.Flags().GetBool("read-only")
		owner, _ := cmd.Flags().GetBool("owner")
		id, _ := cmd.Flags().GetString("id")
		p := map[string]any{"name": args[0], "public_key": key, "profiles": profiles, "owner": owner, "read_only": readonly}
		if id != "" {
			p["id"] = id
		}
		return a.callAndPrint(cmd, "client.register", p)
	}
	g.AddCommand(list, register, a.simpleRPC("revoke CLIENT", "Revoke a controller client identity", "client.revoke", 1, 1, targetParams))
	return g
}
