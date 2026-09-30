// Package guestcli runs the trusted control process inside a hosted container.
package guestcli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/juex-ai/juex/internal/execution/connector"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/spf13/cobra"
)

type Configuration struct {
	Server         string                               `json:"server"`
	Credential     string                               `json:"credential"`
	Environment    execprotocol.Environment             `json:"environment"`
	Grants         map[string][]execprotocol.Capability `json:"grants"`
	StateDirectory string                               `json:"state_directory"`
	CAFile         string                               `json:"ca_file"`
}

func Execute(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	root := &cobra.Command{Use: "juex-guest", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	var configPath string
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if runtime.GOOS != "linux" || os.Geteuid() != 0 {
			return errors.New("hosted guest requires the container control identity on Linux")
		}
		info, err := os.Lstat(configPath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("guest configuration must be a private regular file")
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			return err
		}
		var config Configuration
		if err := json.Unmarshal(data, &config); err != nil {
			return err
		}
		if config.Environment.Kind != "hosted" || len(config.Grants) != 1 {
			return execprotocol.ErrInvalid
		}
		helper, err := os.Executable()
		if err != nil {
			return err
		}
		engine, err := native.Open(native.Config{StateDirectory: config.StateDirectory, EnvironmentID: config.Environment.ID, WorkingDirectory: config.Environment.WorkingDirectory, Grants: config.Grants, ProcessUser: &native.ProcessUser{UID: 1000, GID: 1000, Home: "/home/agent", Helper: helper}})
		if err != nil {
			return err
		}
		defer func() { _ = engine.Close() }()
		var httpClient *http.Client
		if config.CAFile != "" {
			data, err := os.ReadFile(config.CAFile)
			if err != nil {
				return err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(data) {
				return errors.New("invalid hosted endpoint CA")
			}
			httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}}}
		}
		return connector.Run(cmd.Context(), connector.Config{URL: config.Server, Token: config.Credential, Environment: config.Environment, Engine: engine, HTTPClient: httpClient})
	}}
	serve.Flags().StringVar(&configPath, "config", "/var/lib/juex-control/enrollment.json", "Private hosted enrollment configuration")
	var directory string
	file := &cobra.Command{Use: "file-tool", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() == 0 {
			return errors.New("file tools must run as an unprivileged user")
		}
		var request execprotocol.Request
		decoder := json.NewDecoder(io.LimitReader(in, (2<<20)+4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			return err
		}
		if err := request.Validate(); err != nil {
			return err
		}
		if execprotocol.RequiredCapability(request.Kind) != execprotocol.Files {
			return execprotocol.ErrInvalid
		}
		var args native.FileArguments
		if err := json.Unmarshal(request.Arguments, &args); err != nil {
			return err
		}
		if args.WorkingDirectory != "" {
			directory = args.WorkingDirectory
		}
		if !filepath.IsAbs(directory) {
			return execprotocol.ErrInvalid
		}
		return native.RunFileOperation(cmd.Context(), directory, request.Kind, args, out)
	}}
	file.Flags().StringVar(&directory, "working-directory", "/workspace", "Default file tool working directory")
	var transferDirectory, transferPath, direction, sha256 string
	var size int64
	transfer := &cobra.Command{Use: "transfer-tool", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() == 0 {
			return errors.New("file transfers must run as an unprivileged user")
		}
		switch direction {
		case "export":
			return native.RunFileExport(cmd.Context(), transferDirectory, transferPath, out)
		case "import":
			err := native.RunFileImport(cmd.Context(), transferDirectory, transferPath, execprotocol.FileManifest{Size: size, SHA256: sha256}, in)
			code := execprotocol.ErrorCode(err)
			if errors.Is(err, os.ErrPermission) {
				code = "denied"
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = "cancelled"
			}
			return json.NewEncoder(out).Encode(native.FileTransferResult{Error: code})
		default:
			return execprotocol.ErrInvalid
		}
	}}
	transfer.Flags().StringVar(&transferDirectory, "working-directory", "/workspace", "Default working directory")
	transfer.Flags().StringVar(&transferPath, "path", "", "Source or new destination file")
	transfer.Flags().StringVar(&direction, "direction", "", "export or import")
	transfer.Flags().StringVar(&sha256, "sha256", "", "Expected import SHA-256")
	transfer.Flags().Int64Var(&size, "size", 0, "Expected import bytes")
	root.AddCommand(serve, file, transfer)
	return root.ExecuteContext(ctx)
}
