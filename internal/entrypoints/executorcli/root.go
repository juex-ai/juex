// Package executorcli binds a native executor to one Tenant/User enrollment.
package executorcli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/connector"
	"github.com/juex-ai/juex/internal/execution/hostservice"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/version"
	"github.com/spf13/cobra"
)

type pendingPair struct {
	Server       string                     `json:"server"`
	Request      execution.PairRequest      `json:"request"`
	Confirmation execution.PairConfirmation `json:"confirmation"`
}

func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	var state, server, name, workingDirectory string
	var insecure, restart, background bool
	root := &cobra.Command{Use: "juex-executor", Short: "Connect this Linux or macOS user account as an Agent execution environment", SilenceUsage: true, SilenceErrors: true}
	root.Version = version.Version
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().StringVar(&state, "state", "", "Absolute private directory for this Tenant/User enrollment")
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			return errors.New("native executor supports Linux and macOS")
		}
		if !filepath.IsAbs(state) {
			return errors.New("--state must be an absolute private directory")
		}
		state = filepath.Clean(state)
		if err := os.MkdirAll(state, 0700); err != nil {
			return err
		}
		info, err := os.Stat(state)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("--state must be private to the current user")
		}
		return nil
	}
	pair := &cobra.Command{Use: "pair", Short: "Approve device access in the Dashboard and then confirm locally", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := os.Stat(filepath.Join(state, "enrollment.json")); err == nil {
			return errors.New("this directory already holds an enrollment; choose another --state directory for another Tenant/User")
		} else if !os.IsNotExist(err) {
			return err
		}
		origin, err := publicURL(server, insecure)
		if err != nil {
			return err
		}
		if workingDirectory == "" {
			workingDirectory, err = os.UserHomeDir()
			if err != nil {
				return err
			}
		}
		if !filepath.IsAbs(workingDirectory) {
			return errors.New("--working-directory must be absolute")
		}
		if name == "" {
			name, err = os.Hostname()
			if err != nil {
				return err
			}
		}
		pending := pendingPair{}
		path := filepath.Join(state, "pairing.json")
		loadErr := readPrivate(path, &pending)
		if loadErr != nil && !os.IsNotExist(loadErr) {
			return loadErr
		}
		if restart || os.IsNotExist(loadErr) {
			pending.Server = origin
			pending.Confirmation = execution.PairConfirmation{ID: rand.Text(), Secret: rand.Text() + rand.Text(), Credential: rand.Text() + rand.Text()}
			pending.Request = execution.PairRequest{ID: pending.Confirmation.ID, Name: name, OS: runtime.GOOS, WorkingDirectory: workingDirectory, Capabilities: []execprotocol.Capability{execprotocol.Files, execprotocol.Shell, execprotocol.MCP}, PairSecretHash: execution.Digest(pending.Confirmation.Secret), CredentialHash: execution.Digest(pending.Confirmation.Credential)}
			if err := savePrivate(path, pending); err != nil {
				return err
			}
		} else if pending.Server != origin {
			return errors.New("pending pairing belongs to a different server; use --restart to replace the unconfirmed request")
		}
		var response execution.Pairing
		if err := post(cmd.Context(), origin, "/device/pair", pending.Request, &response); err != nil {
			return err
		}
		fmt.Fprintln(out, "Open this link and choose the Tenant, Agents and capabilities:")
		fmt.Fprintln(out, origin+"/pair/"+url.PathEscape(pending.Request.ID))
		for {
			if err := post(cmd.Context(), origin, "/device/pair/poll", map[string]string{"id": pending.Confirmation.ID, "secret": pending.Confirmation.Secret}, &response); err != nil {
				return err
			}
			if response.State == "approved" || response.State == "confirmed" {
				break
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-cmd.Context().Done():
				timer.Stop()
				return cmd.Context().Err()
			case <-timer.C:
			}
		}
		fmt.Fprintf(out, "Tenant: %s\nAccount: %s\nDevice: %s (%s)\nWorking directory: %s\n", response.Owner.TenantName, response.Owner.OwnerEmail, response.Name, response.OS, response.WorkingDirectory)
		for agent, capabilities := range response.Grants {
			fmt.Fprintf(out, "Agent %s: %v\n", agent, capabilities)
		}
		fmt.Fprintln(out, "These Agents may use the approved capabilities with this OS user's permissions. The working directory is not a sandbox.")
		fmt.Fprint(out, "Type yes to authorize this device: ")
		type confirmationInput struct {
			text string
			err  error
		}
		input := make(chan confirmationInput, 1)
		go func() { answer, err := bufio.NewReader(in).ReadString('\n'); input <- confirmationInput{answer, err} }()
		var answer string
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case value := <-input:
			answer = value.text
			if value.err != nil && value.err != io.EOF {
				return value.err
			}
		}
		if strings.TrimSpace(answer) != "yes" {
			return errors.New("local authorization not granted")
		}
		pending.Confirmation.ApprovalNonce = response.ApprovalNonce
		var device execution.Device
		if err := post(cmd.Context(), origin, "/device/pair/confirm", pending.Confirmation, &device); err != nil {
			return err
		}
		if device.Status != "active" {
			return errors.New("enrollment is no longer active")
		}
		if err := savePrivate(filepath.Join(state, "enrollment.json"), connector.Enrollment{Server: origin, Credential: pending.Confirmation.Credential, Device: device, InsecureHTTP: insecure}); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(out, "Paired. Run juex-executor --state", state, "run to connect.")
		return nil
	}}
	pair.Flags().StringVar(&server, "server", "", "Platform HTTPS origin")
	pair.Flags().StringVar(&name, "name", "", "Device display name (defaults to hostname)")
	pair.Flags().StringVar(&workingDirectory, "working-directory", "", "Default command directory; does not restrict OS access")
	pair.Flags().BoolVar(&insecure, "insecure-http", false, "Allow HTTP for an isolated development platform")
	pair.Flags().BoolVar(&restart, "restart", false, "Replace an expired or unconfirmed local pairing request")
	run := &cobra.Command{Use: "run", Short: "Connect in the foreground; network interruptions retain running operations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		var config connector.Enrollment
		if err := readPrivate(filepath.Join(state, "enrollment.json"), &config); err != nil {
			return err
		}
		if _, err := publicURL(config.Server, config.InsecureHTTP); err != nil {
			return err
		}
		engine, err := native.Open(native.Config{StateDirectory: filepath.Join(state, "journal"), EnvironmentID: config.Device.ID, WorkingDirectory: config.Device.WorkingDirectory, Grants: config.Device.Ceiling, HomeDirectory: config.HomeDirectory})
		if err != nil {
			return err
		}
		return runDevice(cmd.Context(), state, config, engine, background, out)
	}}
	run.Flags().BoolVar(&background, "background-log", false, "Write service output to the private rotating log")
	root.AddCommand(pair, run)
	addServiceCommands(root, &state, out)
	return root.ExecuteContext(ctx)
}

func runDevice(ctx context.Context, directory string, config connector.Enrollment, engine *native.Engine, background bool, out io.Writer) (result error) {
	log, err := hostservice.OpenLog(directory)
	if err != nil {
		_ = engine.Close()
		return err
	}
	defer func() { result = errors.Join(result, log.Close()) }()
	writer := io.Writer(log)
	if !background {
		writer = io.MultiWriter(out, log)
	}
	recorder, err := hostservice.Record(directory, config.Device.ID, background)
	if err != nil {
		_ = engine.Close()
		return err
	}
	defer func() { result = errors.Join(result, engine.Close(), recorder.Update("stopped")) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stateErr error
	client, err := config.HTTPClient()
	if err != nil {
		return err
	}
	err = connector.Run(ctx, connector.Config{URL: config.Server, Token: config.Credential, Environment: config.Device.Environment, Engine: engine, HTTPClient: client, InsecureHTTP: config.InsecureHTTP, OnState: func(state string) {
		_, logErr := fmt.Fprintf(writer, "%s Device %s\n", time.Now().UTC().Format(time.RFC3339), state)
		stateErr = errors.Join(stateErr, logErr, recorder.Update(state))
		if stateErr != nil {
			cancel()
		}
	}})
	if err != nil {
		_, logErr := fmt.Fprintf(writer, "%s Executor stopped: %v\n", time.Now().UTC().Format(time.RFC3339), err)
		stateErr = errors.Join(stateErr, logErr)
	}
	return errors.Join(err, stateErr)
}

func addServiceCommands(root *cobra.Command, state *string, out io.Writer) {
	manager := func() (*hostservice.Manager, error) {
		binary, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return hostservice.New(*state, binary)
	}
	paired := func() error {
		var config connector.Enrollment
		return readPrivate(filepath.Join(*state, "enrollment.json"), &config)
	}
	root.AddCommand(&cobra.Command{Use: "start", Short: "Start this enrollment in the OS user service manager", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := paired(); err != nil {
			return err
		}
		m, err := manager()
		if err != nil {
			return err
		}
		status, err := m.Start(cmd.Context())
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(status)
	}})
	root.AddCommand(&cobra.Command{Use: "stop", Short: "Stop the background executor and cancel its operations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := manager()
		if err != nil {
			return err
		}
		if err := m.Stop(cmd.Context()); err != nil {
			return err
		}
		fmt.Fprintln(out, "Executor stopped.")
		return nil
	}})
	root.AddCommand(&cobra.Command{Use: "status", Short: "Show the verified process, connection and autostart status", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		m, err := manager()
		if err != nil {
			return err
		}
		status, err := m.Status()
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(status)
	}})
	var tail int
	logs := &cobra.Command{Use: "logs", Short: "Read recent lines from the private rotating log", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		text, err := hostservice.Logs(*state, tail)
		if err != nil {
			return err
		}
		if text != "" {
			_, err = fmt.Fprintln(out, text)
		}
		return err
	}}
	logs.Flags().IntVar(&tail, "tail", 200, "Number of recent lines (1-2000)")
	root.AddCommand(logs)
	root.AddCommand(&cobra.Command{Use: "autostart enable|disable", Short: "Explicitly enable or disable starting at OS user login", Args: cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs), ValidArgs: []string{"enable", "disable"}, RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "enable" {
			if err := paired(); err != nil {
				return err
			}
		}
		m, err := manager()
		if err != nil {
			return err
		}
		if err := m.Autostart(cmd.Context(), args[0] == "enable"); err != nil {
			return err
		}
		fmt.Fprintf(out, "Autostart %sd. Running executor state is unchanged.\n", args[0])
		return nil
	}})
}

func publicURL(raw string, insecure bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && (!insecure || u.Scheme != "http")) {
		return "", errors.New("--server must be an HTTPS origin; development HTTP requires --insecure-http")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}
func post(ctx context.Context, origin, path string, body, target any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", origin+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure) != nil {
			return fmt.Errorf("device request returned HTTP %d", response.StatusCode)
		}
		if failure.Error == "" {
			return fmt.Errorf("device request returned HTTP %d", response.StatusCode)
		}
		return execprotocol.FromErrorCode(failure.Error)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(target)
}
func readPrivate(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("executor credentials must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func savePrivate(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".enrollment-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
