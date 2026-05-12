package cli

import (
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// schemaCommand is one node in the dumped command tree.
type schemaCommand struct {
	Name        string          `json:"name"`
	Use         string          `json:"use"`
	Short       string          `json:"short,omitempty"`
	Long        string          `json:"long,omitempty"`
	Example     string          `json:"example,omitempty"`
	Flags       []schemaFlag    `json:"flags,omitempty"`
	Subcommands []schemaCommand `json:"subcommands,omitempty"`
}

// schemaFlag is one flag's introspection record.
type schemaFlag struct {
	Name       string `json:"name"` // long form, no leading --
	Shorthand  string `json:"shorthand,omitempty"`
	Type       string `json:"type"`
	Default    string `json:"default,omitempty"`
	Usage      string `json:"usage,omitempty"`
	Required   bool   `json:"required,omitempty"`
	Persistent bool   `json:"persistent,omitempty"` // inherited from a parent
}

func newSchemaCmd(_ *persistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the full command tree (cobra introspection) as JSON",
		Long: `Output a structured JSON description of every juex subcommand and flag.
This is the recommended way for an agent to learn what juex can do without
running --help on each subcommand. Schema is stable; treat it as part of
the contract — additive changes only between minor versions.`,
		Example: `  juex schema | jq '.subcommands[] | .name'
  juex schema | jq '.subcommands[] | select(.name=="run") | .flags'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			tree := dumpCommand(root)
			cmdPrintln(cmd, mustJSON(tree))
			return nil
		},
	}
}

func dumpFlag(f *pflag.Flag, persistent bool) schemaFlag {
	return schemaFlag{
		Name:       f.Name,
		Shorthand:  f.Shorthand,
		Type:       f.Value.Type(),
		Default:    f.DefValue,
		Usage:      f.Usage,
		Persistent: persistent,
	}
}

func dumpCommand(c *cobra.Command) schemaCommand {
	out := schemaCommand{
		Name:    c.Name(),
		Use:     c.Use,
		Short:   c.Short,
		Long:    c.Long,
		Example: c.Example,
	}
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		out.Flags = append(out.Flags, dumpFlag(f, false))
	})
	c.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		out.Flags = append(out.Flags, dumpFlag(f, true))
	})
	sort.Slice(out.Flags, func(i, j int) bool { return out.Flags[i].Name < out.Flags[j].Name })

	subs := c.Commands()
	for _, sub := range subs {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		out.Subcommands = append(out.Subcommands, dumpCommand(sub))
	}
	sort.Slice(out.Subcommands, func(i, j int) bool { return out.Subcommands[i].Name < out.Subcommands[j].Name })
	return out
}
