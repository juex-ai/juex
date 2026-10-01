package managementcli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/maildelivery"
	"github.com/spf13/cobra"
)

func smtpCommand(out io.Writer) *cobra.Command {
	var config maildelivery.Config
	var passwordEnv string
	root := &cobra.Command{Use: "smtp", Short: "Prepare encrypted deployment mail settings"}
	seal := &cobra.Command{
		Use: "seal", Short: "Print an encrypted JUEX_SMTP_CONFIG deployment setting",
		Args: cobra.NoArgs, Annotations: map[string]string{"maintenance": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			config.Password = os.Getenv(passwordEnv)
			if config.Username != "" && config.Password == "" {
				return errors.New("SMTP authentication requires a nonempty password environment variable")
			}
			sealed, err := managed.SealSMTPConfig(os.Getenv("JUEX_MASTER_KEY"), config)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, "JUEX_SMTP_CONFIG="+sealed)
			return err
		},
	}
	seal.Flags().StringVar(&config.Address, "address", "", "SMTP host:port")
	seal.Flags().StringVar(&config.From, "from", "", "Sender email address")
	seal.Flags().StringVar(&config.Username, "username", "", "SMTP authentication user; omit for a trusted relay")
	seal.Flags().StringVar(&config.TLSMode, "tls-mode", "starttls", "starttls, tls, or plain for an unauthenticated local relay")
	seal.Flags().StringVar(&passwordEnv, "password-env", "JUEX_SMTP_CREDENTIAL", "Temporary environment variable containing the password")
	for _, flag := range []string{"address", "from"} {
		if err := seal.MarkFlagRequired(flag); err != nil {
			panic(err)
		}
	}
	root.AddCommand(seal)
	return root
}
