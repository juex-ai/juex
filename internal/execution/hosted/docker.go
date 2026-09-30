package hosted

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const ownershipLabel = "ai.juex.hosted.environment"

var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Config struct {
	Socket          string         `json:"socket"`
	Root            string         `json:"root"`
	WorkspaceRoot   string         `json:"workspace_root"`
	StorageIdentity string         `json:"storage_identity"`
	GuestBinary     string         `json:"guest_binary"`
	Image           string         `json:"image"`
	Pool            netip.Prefix   `json:"pool"`
	Control         netip.AddrPort `json:"control"`
	Server          string         `json:"server"`
	CA              []byte         `json:"-"`
	DNS             []netip.Addr   `json:"dns"`
	Protected       []netip.Prefix `json:"protected"`
	Allow           []Endpoint     `json:"allow"`
}

// Spec is produced by Execution's durable resource record, not by a model or
// public API request. Slot is allocated uniquely in the hosted pool by storage.
type Spec struct {
	EnvironmentID, AgentID, TenantID, UserID, Credential string
	Slot                                                 uint16
	Memory                                               int64
	NanoCPUs                                             int64
	StorageIdentity                                      string
	ProjectID                                            uint32
	WorkspaceBytes, WorkspaceInodes                      int64
	Provisioned                                          bool
}
type Instance struct {
	ID, EnvironmentID, NetworkID, Bridge, Subnet string
	Running                                      bool
}
type Docker struct {
	client    *client.Client
	config    Config
	firewall  Firewall
	mu        sync.Mutex
	guestHash string
}

func New(config Config) (*Docker, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return nil, errors.New("hosted containers require Linux")
	}
	if !filepath.IsAbs(config.Socket) || !filepath.IsAbs(config.Root) || !filepath.IsAbs(config.GuestBinary) || !imageID.MatchString(config.Image) {
		return nil, errors.New("hosted configuration requires absolute paths and a pinned local image ID")
	}
	if !config.Pool.IsValid() || !config.Pool.Addr().Is4() || !config.Pool.Addr().IsPrivate() || config.Pool.Bits() != 16 || config.Pool != config.Pool.Masked() {
		return nil, errors.New("hosted address pool must be a private IPv4 /16")
	}
	server, err := url.Parse(config.Server)
	if err != nil || server.Scheme != "https" || server.Host == "" || server.User != nil || server.RawQuery != "" || server.Fragment != "" || (server.Path != "" && server.Path != "/") {
		return nil, errors.New("hosted device endpoint must use an HTTPS origin")
	}
	port := server.Port()
	if port == "" {
		port = "443"
	}
	if port != strconv.Itoa(int(config.Control.Port())) {
		return nil, errors.New("hosted control port must match its HTTPS origin")
	}
	if _, _, err := resolver(config); err != nil {
		return nil, err
	}
	for _, path := range []string{config.GuestBinary} {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("guest binary must be a regular file not writable by group or others")
		}
	}
	if err := privateDirectory(config.Root, 0700, 0); err != nil {
		return nil, err
	}
	guest, err := os.ReadFile(config.GuestBinary)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(guest)
	c, err := client.New(client.WithHost("unix://" + config.Socket))
	if err != nil {
		return nil, err
	}
	return &Docker{client: c, config: config, guestHash: hex.EncodeToString(digest[:])}, nil
}
func (d *Docker) Close() error { return d.client.Close() }
func (d *Docker) Check(ctx context.Context) error {
	storage, err := storageMount(ctx, d.config)
	if err != nil {
		return err
	}
	_ = storage.Close()
	info, err := d.client.Info(ctx, client.InfoOptions{})
	if err != nil {
		return err
	}
	if info.Info.OSType != "linux" || info.Info.CgroupVersion != "2" {
		return errors.New("hosted execution requires Linux with cgroup v2")
	}
	if _, ok := info.Info.Runtimes["runsc"]; !ok {
		return errors.New("gVisor runsc is required; no alternate runtime is permitted")
	}
	if info.Info.FirewallBackend != nil && info.Info.FirewallBackend.Driver != "iptables" {
		return errors.New("hosted execution requires the Docker iptables firewall backend")
	}
	image, err := d.client.ImageInspect(ctx, d.config.Image)
	if err != nil {
		return err
	}
	if image.Os != "linux" {
		return errors.New("hosted image must target Linux")
	}
	return nil
}
func (d *Docker) policy(spec Spec) (Policy, error) {
	for _, id := range []string{spec.EnvironmentID, spec.AgentID, spec.TenantID, spec.UserID} {
		if _, err := uuid.Parse(id); err != nil {
			return Policy{}, execprotocol.ErrInvalid
		}
	}
	if spec.Slot >= 4096 || spec.Memory < 128<<20 || spec.NanoCPUs < 100000000 || spec.Memory > 64<<30 || spec.NanoCPUs > 64000000000 {
		return Policy{}, execprotocol.ErrInvalid
	}
	hash := sha256.Sum256([]byte(spec.EnvironmentID))
	bridge := "jx" + hex.EncodeToString(hash[:6])
	address := d.config.Pool.Addr().As4()
	offset := uint32(spec.Slot) * 16
	address[2], address[3] = byte(offset>>8), byte(offset)
	_, dns, err := resolver(d.config)
	if err != nil {
		return Policy{}, err
	}
	policy := Policy{Bridge: bridge, Subnet: netip.PrefixFrom(netip.AddrFrom4(address), 28).String(), Control: d.config.Control, HostedPool: d.config.Pool, Protected: d.config.Protected, Allow: append(dns, d.config.Allow...)}
	return policy, policy.validate()
}
func (d *Docker) Ensure(ctx context.Context, spec Spec) (Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(spec.Credential) < 32 {
		return Instance{}, execprotocol.ErrInvalid
	}
	p, err := d.policy(spec)
	if err != nil {
		return Instance{}, err
	}
	if err := d.Check(ctx); err != nil {
		return Instance{}, err
	}
	if err := prepareStorage(ctx, d.config, spec); err != nil {
		return Instance{}, err
	}
	name := "juex-" + spec.EnvironmentID
	// Provisioned records lifecycle progress, not the container configuration.
	fingerprintSpec := spec
	fingerprintSpec.Provisioned = false
	encoded, _ := json.Marshal(struct {
		Spec      Spec
		Config    Config
		GuestHash string
		CAHash    [32]byte
	}{fingerprintSpec, d.config, d.guestHash, sha256.Sum256(d.config.CA)})
	hash := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(hash[:])
	labels := map[string]string{ownershipLabel: spec.EnvironmentID, "ai.juex.agent": spec.AgentID, "ai.juex.spec": fingerprint}
	netResult, err := d.client.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if errdefs.IsNotFound(err) {
		ipv6 := false
		_, err = d.client.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", EnableIPv6: &ipv6, IPAM: &network.IPAM{Config: []network.IPAMConfig{{Subnet: netip.MustParsePrefix(p.Subnet)}}}, Labels: labels, Options: map[string]string{"com.docker.network.bridge.name": p.Bridge, "com.docker.network.bridge.enable_icc": "false", "com.docker.network.bridge.host_binding_ipv4": "127.0.0.1"}})
		if err == nil {
			netResult, err = d.client.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		}
	}
	if err != nil {
		return Instance{}, err
	}
	nw := netResult.Network
	if nw.Labels[ownershipLabel] != spec.EnvironmentID || nw.Driver != "bridge" || nw.EnableIPv6 || nw.Options["com.docker.network.bridge.name"] != p.Bridge || nw.Options["com.docker.network.bridge.enable_icc"] != "false" || len(nw.IPAM.Config) != 1 || nw.IPAM.Config[0].Subnet.String() != p.Subnet {
		return Instance{}, errors.New("hosted network identity or isolation configuration changed")
	}
	if err := d.firewall.Apply(ctx, p); err != nil {
		return Instance{}, err
	}
	result := Instance{EnvironmentID: spec.EnvironmentID, NetworkID: nw.ID, Bridge: p.Bridge, Subnet: p.Subnet}
	inspected, err := d.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		c := inspected.Container
		if c.Config.Labels[ownershipLabel] != spec.EnvironmentID || c.Config.Labels["ai.juex.spec"] != fingerprint || c.HostConfig.Runtime != "runsc" || c.HostConfig.Privileged || !c.HostConfig.ReadonlyRootfs {
			return result, errors.New("hosted container configuration changed; explicit rebuild required")
		}
		result.ID, result.Running = c.ID, c.State.Running
	} else if errdefs.IsNotFound(err) {
		root := filepath.Join(d.config.Root, spec.EnvironmentID)
		if err := privateDirectory(root, 0700, 0); err != nil {
			return result, err
		}
		for _, entry := range []struct {
			name string
			uid  int
		}{{"control", 0}} {
			if err := privateDirectory(filepath.Join(root, entry.name), 0700, entry.uid); err != nil {
				return result, err
			}
		}
		config := map[string]any{"server": d.config.Server, "credential": spec.Credential, "environment": execprotocol.Environment{ID: spec.EnvironmentID, Kind: "hosted", OS: "linux", Name: "Hosted workspace", WorkingDirectory: "/workspace", PermissionMode: "gvisor"}, "grants": map[string][]execprotocol.Capability{spec.AgentID: {execprotocol.Files, execprotocol.Shell, execprotocol.MCP}}, "state_directory": "/var/lib/juex-control/journal"}
		if len(d.config.CA) > 0 {
			config["ca_file"] = "/var/lib/juex-control/ca.pem"
			if err := privateFile(filepath.Join(root, "control", "ca.pem"), d.config.CA); err != nil {
				return result, err
			}
		}
		data, err := json.Marshal(config)
		if err != nil {
			return result, err
		}
		if err := privateFile(filepath.Join(root, "control", "enrollment.json"), data); err != nil {
			return result, err
		}
		// Docker's embedded loopback resolver is outside gVisor's network stack.
		// Bind an explicit resolver file; DNS authority is limited to port 53.
		resolv, _, _ := resolver(d.config)
		resolverPath := filepath.Join(root, "control", "resolv.conf")
		if err := privateFile(resolverPath, []byte(resolv)); err != nil {
			return result, err
		}
		if err := os.Chmod(resolverPath, 0644); err != nil {
			return result, err
		}
		pids := int64(256)
		server, _ := url.Parse(d.config.Server)
		extraHosts := []string{server.Hostname() + ":" + d.config.Control.Addr().String()}
		mounts := []mount.Mount{{Type: mount.TypeBind, Source: d.config.GuestBinary, Target: "/usr/local/bin/juex-guest", ReadOnly: true}, {Type: mount.TypeBind, Source: filepath.Join(root, "control"), Target: "/var/lib/juex-control"}, {Type: mount.TypeBind, Source: filepath.Join(d.config.WorkspaceRoot, spec.EnvironmentID, "workspace"), Target: "/workspace"}, {Type: mount.TypeBind, Source: filepath.Join(d.config.WorkspaceRoot, spec.EnvironmentID, "home"), Target: "/home/agent"}}
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: resolverPath, Target: "/etc/resolv.conf", ReadOnly: true})
		created, err := d.client.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: d.config.Image, User: "0:0", Entrypoint: []string{"/usr/local/bin/juex-guest"}, Cmd: []string{"serve"}, WorkingDir: "/", Labels: labels, Env: []string{"HOME=/home/agent", "PATH=/home/agent/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}}, HostConfig: &container.HostConfig{Runtime: "runsc", ExtraHosts: extraHosts, NetworkMode: container.NetworkMode(nw.ID), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"SETUID", "SETGID", "KILL"}, SecurityOpt: []string{"no-new-privileges:true"}, Mounts: mounts, Tmpfs: map[string]string{"/tmp": "rw,exec,nosuid,nodev,mode=1777,size=268435456"}, Sysctls: map[string]string{"net.ipv6.conf.all.disable_ipv6": "1", "net.ipv6.conf.default.disable_ipv6": "1"}, Resources: container.Resources{Memory: spec.Memory, MemorySwap: spec.Memory, NanoCPUs: spec.NanoCPUs, PidsLimit: &pids}, LogConfig: container.LogConfig{Type: "local", Config: map[string]string{"max-size": "2m", "max-file": "4"}}, RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled}}, NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{nw.ID: {}}}})
		if err != nil {
			return result, err
		}
		result.ID = created.ID
	} else {
		return result, err
	}
	if !result.Running {
		if _, err := d.client.ContainerStart(ctx, result.ID, client.ContainerStartOptions{}); err != nil {
			return result, fmt.Errorf("start gVisor environment: %w", err)
		}
		result.Running = true
	}
	return result, nil
}
func (d *Docker) Stop(ctx context.Context, spec Spec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.policy(spec); err != nil {
		return err
	}
	c, err := d.client.ContainerInspect(ctx, "juex-"+spec.EnvironmentID, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.Container.Config.Labels[ownershipLabel] != spec.EnvironmentID {
		return execprotocol.ErrDenied
	}
	timeout := 20
	_, err = d.client.ContainerStop(ctx, c.Container.ID, client.ContainerStopOptions{Timeout: &timeout})
	return err
}

// RemoveContainer preserves durable volumes. Purging data is separately
// coordinated with cancellation and backup retention by the owning service.
func (d *Docker) RemoveContainer(ctx context.Context, spec Spec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, err := d.policy(spec)
	if err != nil {
		return err
	}
	name := "juex-" + spec.EnvironmentID
	c, err := d.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	if err == nil {
		if c.Container.Config.Labels[ownershipLabel] != spec.EnvironmentID || c.Container.State.Running {
			return errors.New("hosted container must be owned and stopped before removal")
		}
		if _, err := d.client.ContainerRemove(ctx, c.Container.ID, client.ContainerRemoveOptions{}); err != nil {
			return err
		}
	}
	nw, err := d.client.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	if err == nil {
		if nw.Network.Labels[ownershipLabel] != spec.EnvironmentID {
			return execprotocol.ErrDenied
		}
		if _, err := d.client.NetworkRemove(ctx, nw.Network.ID, client.NetworkRemoveOptions{}); err != nil {
			return err
		}
	}
	return d.firewall.Remove(ctx, p)
}

func privateDirectory(path string, mode os.FileMode, uid int) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		if err := os.Chown(path, uid, uid); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("hosted storage must not be a symlink or non-directory")
	}
	if uid == 0 && (info.Mode().Perm()&0077 != 0 || !controlOwned(info)) {
		return errors.New("hosted control directory must be private and owned by root")
	}
	return nil
}
func privateFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("hosted control file must be regular")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".control-")
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
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func resolver(config Config) (string, []Endpoint, error) {
	if len(config.DNS) == 0 || len(config.DNS) > 3 {
		return "", nil, errors.New("hosted DNS requires one to three routable IPv4 resolvers")
	}
	var content strings.Builder
	var endpoints []Endpoint
	for _, address := range config.DNS {
		if !address.Is4() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() || config.Pool.Contains(address) {
			return "", nil, errors.New("hosted DNS resolver cannot target loopback, metadata or an Agent")
		}
		for _, prefix := range config.Protected {
			if prefix.Contains(address) {
				return "", nil, errors.New("hosted DNS resolver overlaps platform protection")
			}
		}
		fmt.Fprintf(&content, "nameserver %s\n", address)
		endpoints = append(endpoints, Endpoint{Address: address, Protocol: "udp", Port: 53}, Endpoint{Address: address, Protocol: "tcp", Port: 53})
	}
	content.WriteString("options timeout:2 attempts:2\n")
	return content.String(), endpoints, nil
}
