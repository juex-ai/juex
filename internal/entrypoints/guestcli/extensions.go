package guestcli

import (
	"errors"
	"os"
	"syscall"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/spf13/cobra"
)

func extensionCommand() *cobra.Command {
	var base, relative, environment, agent, binding, directory string
	command := &cobra.Command{Use: "extension-exec", Hidden: true, Args: cobra.MinimumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if os.Geteuid() == 0 {
			return errors.New("extension commands must run as an unprivileged user")
		}
		values, err := native.ExtensionProcessEnvironment(base, relative, environment, agent, &execprotocol.ExtensionContext{BindingID: binding, Directory: directory}, os.Environ())
		if err != nil {
			return err
		}
		working, err := os.Getwd()
		if err != nil {
			return err
		}
		executable, err := native.ExtensionExecutable(args[0], working, values)
		if err != nil {
			return err
		}
		return syscall.Exec(executable, args, values)
	}}
	command.Flags().StringVar(&base, "base", "", "Trusted data parent")
	command.Flags().StringVar(&relative, "relative", "", "Trusted data path")
	command.Flags().StringVar(&environment, "environment", "", "Environment identity")
	command.Flags().StringVar(&agent, "agent", "", "Agent identity")
	command.Flags().StringVar(&binding, "binding", "", "Binding identity")
	command.Flags().StringVar(&directory, "directory", "", "Installed resource directory")
	return command
}
