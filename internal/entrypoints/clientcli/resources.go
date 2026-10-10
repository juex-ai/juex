package clientcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"github.com/spf13/cobra"
)

func addResources(root *cobra.Command, command commandFactory, in io.Reader) {
	fleet := &cobra.Command{Use: "fleet", Short: "Manage the selected owner's Fleet"}
	fleet.AddCommand(ownerRead(command, "show", "Inspect Fleet settings and Agents", "/fleet"))
	fleet.AddCommand(ownerJSON(command, "configure", "Set Fleet settings with their current version", "PUT", "/fleet/settings", in))
	root.AddCommand(fleet)
	root.AddCommand(command("models", "List models available to this tenant", cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		path, err := c.tenantPath(cmd.Context())
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "GET", path+"/models", nil)
	}))
	agents := &cobra.Command{Use: "agent", Short: "Manage Agents using stable resource IDs"}
	agents.AddCommand(command("list", "List the selected owner's Agents", cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		path, err := c.ownerPath(cmd.Context())
		if err != nil {
			return nil, err
		}
		data, err := c.request(cmd.Context(), "GET", path+"/fleet", nil)
		if err != nil {
			return nil, err
		}
		var fleet management.FleetOverview
		if err := json.Unmarshal(data, &fleet); err != nil {
			return nil, err
		}
		return fleet.Agents, nil
	}))
	agents.AddCommand(command("get ID", "Inspect an Agent", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		path, err := c.agentPath(cmd.Context(), args[0])
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "GET", path, nil)
	}))
	agents.AddCommand(ownerJSON(command, "create", "Create an Agent from AgentConfig JSON", "POST", "/agents", in))
	var configFile string
	configure := command("configure ID", "Replace AgentConfig using JSON including its current version", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		path, err := c.agentPath(cmd.Context(), args[0])
		if err != nil {
			return nil, err
		}
		body, err := readJSONFile(configFile, in)
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "PUT", path, body)
	})
	configure.Flags().StringVar(&configFile, "data-file", "-", "Configuration JSON file; - reads stdin")
	agents.AddCommand(configure)
	agents.AddCommand(command("environments ID", "List an Agent's authorized environments and default location", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		path, err := c.agentPath(cmd.Context(), args[0])
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "GET", path+"/environments", nil)
	}))
	agents.AddCommand(command("environment ID", "Inspect an Agent's default environment configuration", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		path, err := c.agentPath(cmd.Context(), args[0])
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "GET", path+"/default-environment", nil)
	}))
	var environmentFile string
	configureEnvironment := command("configure-environment ID", "Set environment_id, working_directory and current version using JSON; empty environment_id selects the deployment default", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		path, err := c.agentPath(cmd.Context(), args[0])
		if err != nil {
			return nil, err
		}
		body, err := readJSONFile(environmentFile, in)
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "PUT", path+"/default-environment", body)
	})
	configureEnvironment.Flags().StringVar(&environmentFile, "data-file", "-", "Default environment JSON file; - reads stdin")
	agents.AddCommand(configureEnvironment)
	for _, verb := range []string{"archive", "restore"} {
		var version int64
		cmd := command(verb+" ID", verb+" an Agent without deleting its history", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
			if version < 1 {
				return nil, errors.New("--version is required")
			}
			path, err := c.agentPath(cmd.Context(), args[0])
			if err != nil {
				return nil, err
			}
			return c.request(cmd.Context(), "POST", path+"/archive", map[string]any{"version": version, "archived": verb == "archive"})
		})
		cmd.Flags().Int64Var(&version, "version", 0, "Current Agent version")
		agents.AddCommand(cmd)
	}
	root.AddCommand(agents)
	members := &cobra.Command{Use: "member", Short: "Tenant administrator member and invitation operations"}
	members.AddCommand(tenantRead(command, "list", "List tenant members", "/members"))
	members.AddCommand(tenantRead(command, "invitations", "List invitation state and links", "/invitations"))
	var inviteFile string
	invite := command("invite", "Invite an email with role using JSON", cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		path, err := c.tenantPath(cmd.Context())
		if err != nil {
			return nil, err
		}
		body, err := readJSONFile(inviteFile, in)
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "POST", path+"/invitations", body)
	})
	invite.Flags().StringVar(&inviteFile, "data-file", "-", "Invitation JSON file; - reads stdin")
	members.AddCommand(invite)
	var memberFile string
	change := command("configure ID", "Set a member role and status using JSON", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		id, err := checkedID(args[0])
		if err != nil {
			return nil, err
		}
		path, err := c.tenantPath(cmd.Context())
		if err != nil {
			return nil, err
		}
		body, err := readJSONFile(memberFile, in)
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "PATCH", path+"/members/"+id, body)
	})
	change.Flags().StringVar(&memberFile, "data-file", "-", "Membership JSON file; - reads stdin")
	members.AddCommand(change)
	root.AddCommand(members)
	addThreads(root, command, in)
	for _, name := range []string{"memory", "calendar"} {
		app := &cobra.Command{Use: name, Short: "Inspect and manage Fleet " + name}
		app.AddCommand(ownerRead(command, "status", "Inspect application state", "/"+name))
		app.AddCommand(ownerJSON(command, "configure", "Change versioned application configuration", "PUT", "/"+name, in))
		if name == "memory" {
			app.AddCommand(ownerRead(command, "entries", "Search knowledge; use --query for query parameters", "/memory/entries"))
			app.AddCommand(ownerRead(command, "reviews", "List review receipts", "/memory/reviews"))
			app.AddCommand(ownerJSON(command, "administer", "Correct or forget knowledge with an explicit JSON request", "POST", "/memory/administer", in))
		} else {
			app.AddCommand(ownerRead(command, "schedules", "List shared schedules", "/calendar/schedules"))
			app.AddCommand(ownerRead(command, "occurrences", "List occurrence results and external-stop state", "/calendar/occurrences"))
			app.AddCommand(ownerJSON(command, "change", "Create or change a schedule with an explicit JSON request", "POST", "/calendar/changes", in))
		}
		root.AddCommand(app)
	}
	root.AddCommand(ownerRead(command, "usage", "Report owner token usage; --query supports from, until, group", "/usage"))
	root.AddCommand(ownerRead(command, "devices", "List the selected owner's devices", "/devices"))
	root.AddCommand(tenantRead(command, "notifications", "List your personal Inbox", "/notifications"))
	purge := &cobra.Command{Use: "purge", Short: "Inspect or request irreversible cleanup"}
	purge.AddCommand(ownerRead(command, "list", "List cleanup jobs and unconfirmed external stops", "/purges"))
	for _, kind := range []string{"agent", "fleet"} {
		var version int64
		var confirm, requestID string
		use := "agent AGENT_ID"
		if kind == "fleet" {
			use = "fleet OWNER_ID"
		}
		cleanup := command(use, "Permanently clean an archived Agent or removed member's Fleet", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
			id, err := checkedID(args[0])
			if err != nil {
				return nil, err
			}
			if confirm != id || version < 1 {
				return nil, errors.New("--confirm must repeat the target ID and --version must identify its current version")
			}
			if requestID == "" {
				requestID = uuid.NewString()
			}
			body := management.PurgeRequest{ID: requestID, Version: version}
			if kind == "agent" {
				body.AgentID = id
			} else {
				owner, err := c.ownerID()
				if err != nil {
					return nil, err
				}
				if owner != id {
					return nil, errors.New("fleet cleanup ID must equal the selected --owner")
				}
			}
			path, err := c.ownerPath(cmd.Context())
			if err != nil {
				return nil, err
			}
			data, err := c.request(cmd.Context(), "POST", path+"/purges", body)
			if err != nil {
				return nil, fmt.Errorf("cleanup request %s: %w", requestID, err)
			}
			return data, nil
		})
		cleanup.Flags().Int64Var(&version, "version", 0, "Current Agent version or removed membership version")
		cleanup.Flags().StringVar(&confirm, "confirm", "", "Repeat the target UUID to confirm irreversible removal")
		cleanup.Flags().StringVar(&requestID, "request-id", "", "Stable cleanup UUID; reuse after an uncertain response")
		purge.AddCommand(cleanup)
	}
	root.AddCommand(purge)
}

func ownerRead(command commandFactory, use, short, suffix string) *cobra.Command {
	var query string
	cmd := command(use, short, cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		path, err := c.ownerPath(cmd.Context())
		if err != nil {
			return nil, err
		}
		parsed, err := url.ParseQuery(query)
		if err != nil {
			return nil, err
		}
		if suffix == "/usage" && !parsed.Has("group") {
			parsed.Set("group", "day")
		}
		path += suffix
		if len(parsed) > 0 {
			path += "?" + parsed.Encode()
		}
		return c.request(cmd.Context(), "GET", path, nil)
	})
	cmd.Flags().StringVar(&query, "query", "", "URL query parameters, for example group=month&from=2026-10-01&until=2026-10-31")
	return cmd
}

func tenantRead(command commandFactory, use, short, suffix string) *cobra.Command {
	return command(use, short, cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		path, err := c.tenantPath(cmd.Context())
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "GET", path+suffix, nil)
	})
}

func ownerJSON(command commandFactory, use, short, method, suffix string, in io.Reader) *cobra.Command {
	var file string
	cmd := command(use, short, cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		path, err := c.ownerPath(cmd.Context())
		if err != nil {
			return nil, err
		}
		body, err := readJSONFile(file, in)
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), method, path+suffix, body)
	})
	cmd.Flags().StringVar(&file, "data-file", "-", "Request JSON file; - reads stdin")
	return cmd
}

func addThreads(root *cobra.Command, command commandFactory, in io.Reader) {
	var agent string
	threads := &cobra.Command{Use: "thread", Short: "Manage persistent Main and Worker conversations"}
	threads.PersistentFlags().StringVar(&agent, "agent", "", "Agent UUID (required)")
	threads.AddCommand(command("list", "List Main and Worker Threads", cobra.NoArgs, true, func(cmd *cobra.Command, c *client, _ []string) (any, error) {
		path, err := c.agentPath(cmd.Context(), agent)
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "GET", path+"/threads", nil)
	}))
	var file, requestID string
	send := command("send THREAD_ID", "Durably enqueue text; completion happens independently", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		id, err := checkedID(args[0])
		if err != nil {
			return nil, err
		}
		path, err := c.agentPath(cmd.Context(), agent)
		if err != nil {
			return nil, err
		}
		reader := in
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			reader = f
		}
		text, err := io.ReadAll(io.LimitReader(reader, 64<<10+1))
		if err != nil {
			return nil, err
		}
		if len(text) == 0 || len(text) > 64<<10 || !utf8.Valid(text) {
			return nil, errors.New("input must be valid UTF-8 between 1 byte and 64 KiB")
		}
		if requestID == "" {
			requestID = uuid.NewString()
		}
		data, err := c.request(cmd.Context(), "POST", path+"/inputs", managedruntime.InputRequest{RequestID: requestID, ThreadID: id, Text: string(text)})
		if err != nil {
			return nil, fmt.Errorf("input request %s: %w", requestID, err)
		}
		return data, nil
	})
	send.Flags().StringVar(&file, "text-file", "-", "UTF-8 message file; - reads stdin")
	send.Flags().StringVar(&requestID, "request-id", "", "Stable input identity; reuse after an uncertain response")
	threads.AddCommand(send)
	var after int64
	events := command("events THREAD_ID", "Read a page of durable conversation events", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
		id, err := checkedID(args[0])
		if err != nil {
			return nil, err
		}
		path, err := c.agentPath(cmd.Context(), agent)
		if err != nil {
			return nil, err
		}
		return c.request(cmd.Context(), "GET", path+"/threads/"+id+"/events?after="+strconv.FormatInt(after, 10), nil)
	})
	events.Flags().Int64Var(&after, "after", 0, "Last seen durable sequence")
	threads.AddCommand(events)
	for _, operation := range []string{"worker", "compact", "reset-context"} {
		var requestID, detail string
		cmd := command(operation+" THREAD_ID", "Create a Worker, compact, or reset idle Thread context with a stable request identity", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
			id, err := checkedID(args[0])
			if err != nil {
				return nil, err
			}
			path, err := c.agentPath(cmd.Context(), agent)
			if err != nil {
				return nil, err
			}
			if requestID == "" {
				requestID = uuid.NewString()
			}
			action := "compact"
			body := map[string]string{"request_id": requestID, "focus": detail}
			if operation == "worker" {
				action = "workers"
				body = map[string]string{"request_id": requestID, "name": detail}
			}
			if operation == "reset-context" {
				action = operation
				body = map[string]string{"request_id": requestID}
			}
			data, err := c.request(cmd.Context(), "POST", path+"/threads/"+id+"/"+action, body)
			if err != nil {
				return nil, fmt.Errorf("%s request %s: %w", operation, requestID, err)
			}
			return data, nil
		})
		cmd.Flags().StringVar(&requestID, "request-id", "", "Stable request identity; reuse after an uncertain response")
		switch operation {
		case "worker":
			cmd.Flags().StringVar(&detail, "name", "", "Worker name")
		case "compact":
			cmd.Flags().StringVar(&detail, "focus", "", "Optional compaction focus")
		}
		threads.AddCommand(cmd)
	}
	for _, verb := range []string{"stop", "archive", "restore"} {
		threads.AddCommand(command(verb+" THREAD_ID", verb+" a Thread", cobra.ExactArgs(1), true, func(cmd *cobra.Command, c *client, args []string) (any, error) {
			id, err := checkedID(args[0])
			if err != nil {
				return nil, err
			}
			path, err := c.agentPath(cmd.Context(), agent)
			if err != nil {
				return nil, err
			}
			action := "archive"
			body := map[string]any{"archived": verb == "archive"}
			if verb == "stop" {
				action = "cancel"
				body = map[string]any{}
			}
			return c.request(cmd.Context(), "POST", path+"/threads/"+id+"/"+action, body)
		}))
	}
	root.AddCommand(threads)
}
