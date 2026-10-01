package clientcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type action func(*cobra.Command, *client, []string) (any, error)

func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	var o options
	root := &cobra.Command{Use: "juex", Short: "Manage Agents on a JueX platform", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().StringVar(&o.server, "server", os.Getenv("JUEX_SERVER"), "Public HTTPS platform origin (or JUEX_SERVER)")
	root.PersistentFlags().StringVar(&o.sessionFile, "session-file", "", "Private login file; defaults to an origin-specific OS config path")
	root.PersistentFlags().StringVar(&o.tenant, "tenant", "", "Tenant UUID; defaults to the selected or sole active membership")
	root.PersistentFlags().StringVar(&o.owner, "owner", "", "Resource owner UUID for tenant administration; defaults to yourself")
	root.PersistentFlags().BoolVar(&o.insecure, "insecure-http", false, "Allow HTTP for an explicit development deployment")
	command := func(use, short string, args cobra.PositionalArgs, auth bool, fn action) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.open(auth)
			if err != nil {
				return err
			}
			value, err := fn(cmd, c, args)
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(value)
		}}
	}
	var email string
	var passwordStdin bool
	login := command("login", "Sign in; read the password from stdin", cobra.NoArgs, false, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		if !passwordStdin || email == "" {
			return nil, errors.New("--email and --password-stdin are required")
		}
		password, err := io.ReadAll(io.LimitReader(in, 4097))
		if err != nil {
			return nil, err
		}
		if len(password) > 4096 {
			return nil, errors.New("password input too long")
		}
		password = bytes.TrimSuffix(bytes.TrimSuffix(password, []byte("\n")), []byte("\r"))
		data, err := c.request(cmd.Context(), "POST", "/auth/login", map[string]string{"email": email, "password": string(password)})
		if err != nil {
			return nil, err
		}
		var session loginSession
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, err
		}
		if session.Token == "" {
			return nil, errors.New("server did not return a CLI session")
		}
		session.Origin = c.origin
		c.session = session
		if err := c.saveSession(); err != nil {
			return nil, err
		}
		return session.User, nil
	})
	login.Flags().StringVar(&email, "email", "", "Account email")
	login.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read a password from stdin without including it in argv")
	root.AddCommand(login)
	root.AddCommand(command("logout", "Revoke this login and remove its local credential", cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		data, err := c.request(cmd.Context(), "POST", "/auth/logout", struct{}{})
		if err != nil {
			return nil, err
		}
		return data, os.Remove(c.sessionFile)
	}))
	root.AddCommand(command("whoami", "Inspect the signed-in account", cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		return c.request(cmd.Context(), "GET", "/auth/session", nil)
	}))
	tenant := &cobra.Command{Use: "tenant", Short: "List and select your tenant memberships"}
	tenant.AddCommand(command("list", "List active memberships", cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		return c.request(cmd.Context(), "GET", "/tenants", nil)
	}))
	tenant.AddCommand(command("use ID", "Select an active tenant for this server", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		c.tenant = args[0]
		id, err := c.tenantID(cmd.Context())
		if err != nil {
			return nil, err
		}
		c.session.TenantID = id
		return map[string]string{"tenant_id": id}, c.saveSession()
	}))
	root.AddCommand(tenant)
	addResources(root, command, in)
	var dataFile string
	request := command("request METHOD PATH", "Call a public resource API; PATH starts with /tenants", cobra.ExactArgs(2), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		method := strings.ToUpper(args[0])
		if method != "GET" && method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
			return nil, errors.New("unsupported HTTP method")
		}
		if !strings.HasPrefix(args[1], "/tenants/") && args[1] != "/tenants" {
			return nil, errors.New("request supports resource APIs; use login/logout for authentication")
		}
		var body any
		if dataFile != "" {
			raw, err := readJSONFile(dataFile, in)
			if err != nil {
				return nil, err
			}
			body = raw
		}
		return c.request(cmd.Context(), method, args[1], body)
	})
	request.Flags().StringVar(&dataFile, "data-file", "", "JSON request file; - reads stdin")
	root.AddCommand(request)
	return root.ExecuteContext(ctx)
}

type commandFactory func(string, string, cobra.PositionalArgs, bool, action) *cobra.Command

func readJSONFile(path string, in io.Reader) (json.RawMessage, error) {
	reader := in
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		reader = f
	}
	data, err := io.ReadAll(io.LimitReader(reader, 2<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 || !json.Valid(data) {
		return nil, errors.New("request file must contain one JSON value, at most 2 MiB")
	}
	return data, nil
}
