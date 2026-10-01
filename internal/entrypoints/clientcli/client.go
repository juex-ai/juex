// Package clientcli manages remote JueX resources through the public HTTP API.
package clientcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/management"
)

type options struct {
	server, sessionFile, tenant, owner string
	insecure                           bool
}

type client struct {
	options
	origin  string
	session loginSession
	http    *http.Client
}

func (o options) open(authenticated bool) (*client, error) {
	origin, err := management.PublicOrigin(o.server, o.insecure)
	if err != nil {
		return nil, err
	}
	if o.sessionFile == "" {
		o.sessionFile, err = defaultSessionFile(origin)
		if err != nil {
			return nil, err
		}
	}
	c := &client{options: o, origin: origin, http: &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	c.session, err = readSession(o.sessionFile, origin)
	if err != nil {
		return nil, err
	}
	if authenticated && (c.session.Token == "" || !c.session.ExpiresAt.After(time.Now())) {
		return nil, errors.New("login required; run juex login --email EMAIL --password-stdin")
	}
	return c, nil
}

func (c *client) request(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	ref, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || ref.IsAbs() || ref.Host != "" || ref.Fragment != "" {
		return nil, errors.New("API path must be relative to the selected server")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.origin+"/api"+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Juex-Client", "cli")
	if c.session.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.session.Token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, errors.New("response too large; request a smaller page")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if len(failure.Error) > 1024 {
			failure.Error = ""
		}
		return nil, fmt.Errorf("JueX API returned %d: %s", response.StatusCode, failure.Error)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage("null"), nil
	}
	if !json.Valid(data) {
		return nil, errors.New("expected a JSON API response")
	}
	return data, nil
}

func (c *client) tenantID(ctx context.Context) (string, error) {
	wanted := c.tenant
	if wanted == "" {
		wanted = c.session.TenantID
	}
	data, err := c.request(ctx, "GET", "/tenants", nil)
	if err != nil {
		return "", err
	}
	var tenants []management.TenantAccess
	if err := json.Unmarshal(data, &tenants); err != nil {
		return "", err
	}
	if wanted == "" && len(tenants) == 1 {
		return tenants[0].ID, nil
	}
	for _, tenant := range tenants {
		if wanted == tenant.ID {
			return wanted, nil
		}
	}
	return "", errors.New("select an active tenant with --tenant ID or juex tenant use ID")
}

func (c *client) ownerID() (string, error) {
	if c.owner != "" {
		return checkedID(c.owner)
	}
	return checkedID(c.session.User.ID)
}

func checkedID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return "", errors.New("resource selectors require a canonical UUID")
	}
	return value, nil
}

func (c *client) tenantPath(ctx context.Context) (string, error) {
	id, err := c.tenantID(ctx)
	return "/tenants/" + id, err
}

func (c *client) ownerPath(ctx context.Context) (string, error) {
	prefix, err := c.tenantPath(ctx)
	if err != nil {
		return "", err
	}
	id, err := c.ownerID()
	return prefix + "/users/" + id, err
}

func (c *client) agentPath(ctx context.Context, agent string) (string, error) {
	id, err := checkedID(agent)
	if err != nil {
		return "", err
	}
	prefix, err := c.tenantPath(ctx)
	return prefix + "/agents/" + id, err
}
