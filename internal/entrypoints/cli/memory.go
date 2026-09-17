package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	"github.com/spf13/cobra"
)

func newMemoryCmd() *cobra.Command {
	root := &cobra.Command{Use: "memory", Short: "Inspect and explicitly administer this Fleet's shared Memory"}
	root.AddCommand(&cobra.Command{Use: "serve", Hidden: true, Args: usageArgs(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error { return app.RunMemoryService(cmd.Context()) }})
	for _, operation := range []string{"status", "search", "read", "result", "admin"} {
		op := operation
		file := ""
		service := ""
		cmd := &cobra.Command{Use: op, Args: usageArgs(cobra.NoArgs)}
		cmd.Flags().StringVar(&service, "service", "memory", "Fleet Memory service identity")
		switch op {
		case "status":
			cmd.Short = "Inspect shared Memory readiness and work counts"
		case "search":
			cmd.Use = "search [query]"
			cmd.Short = "Search all user-visible shared Memory"
			cmd.Args = usageArgs(cobra.MaximumNArgs(1))
		case "read", "result":
			cmd.Use = op + " <id>"
			cmd.Short = "Read Memory " + op
			cmd.Args = usageArgs(cobra.ExactArgs(1))
		case "admin":
			cmd.Short = "Apply an explicit user correction, deletion, no-store or relearning request"
			cmd.Flags().StringVar(&file, "file", "", "AdminRequest JSON file, or - for stdin (required)")
			_ = cmd.MarkFlagRequired("file")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if err := serviceendpoint.ValidateID(service); err != nil {
				return &usageError{msg: err.Error()}
			}
			home, err := config.EffectiveHomeDir()
			if err != nil {
				return err
			}
			fleet, err := serviceendpoint.FleetID(home)
			if err != nil {
				return err
			}
			caller := memoryclient.Caller{FleetID: fleet, AgentID: "user", ThreadID: "0", Profile: memoryclient.ProfileUser}
			client := memoryclient.New(serviceendpoint.FileResolver{Home: home, Fleet: fleet}, service, caller)
			var result any
			switch op {
			case "status":
				result, err = client.Status(cmd.Context(), caller)
			case "search":
				query := ""
				if len(args) > 0 {
					query = args[0]
				}
				result, err = client.Search(cmd.Context(), caller, memoryclient.Query{Text: query})
			case "read":
				result, err = client.Read(cmd.Context(), caller, memoryclient.ReadRequest{ID: args[0]})
			case "result":
				result, err = client.Result(cmd.Context(), caller, args[0])
			case "admin":
				reader := cmd.InOrStdin()
				if file != "-" {
					f, e := os.Open(file)
					if e != nil {
						return e
					}
					defer f.Close()
					reader = f
				}
				data, e := io.ReadAll(io.LimitReader(reader, 256*1024+1))
				if e != nil {
					return e
				}
				if len(data) > 256*1024 {
					return fmt.Errorf("memory admin request exceeds 256 KiB")
				}
				var request memoryclient.AdminRequest
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.DisallowUnknownFields()
				if e := decoder.Decode(&request); e != nil {
					return e
				}
				result, err = client.Admin(cmd.Context(), caller, request)
			}
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		root.AddCommand(cmd)
	}
	return root
}
