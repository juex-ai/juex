// Package hosted manages the Linux containers owned by Execution.
package hosted

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// Endpoint exceptions are deliberately address/transport/port scoped. Domain
// resolution does not grant authority to whichever address a name later returns.
type Endpoint struct {
	Address  netip.Addr `json:"address"`
	Protocol string     `json:"protocol"`
	Port     uint16     `json:"port"`
}
type Policy struct {
	Bridge, Subnet string
	Control        netip.AddrPort
	HostedPool     netip.Prefix
	Protected      []netip.Prefix
	Allow          []Endpoint
}

type Firewall struct{ mu sync.Mutex }

var bridgeName = regexp.MustCompile(`^jx[0-9a-f]{12}$`)
var blockedNetworks = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"}

func (p Policy) validate() error {
	if !bridgeName.MatchString(p.Bridge) || !p.Control.IsValid() || !p.Control.Addr().Is4() || p.Control.Port() == 0 || !p.HostedPool.IsValid() || !p.HostedPool.Addr().Is4() || !p.HostedPool.Addr().IsPrivate() {
		return errors.New("invalid hosted network policy")
	}
	subnet, err := netip.ParsePrefix(p.Subnet)
	if err != nil || !subnet.Addr().Is4() || !p.HostedPool.Contains(subnet.Addr()) || subnet.Bits() < p.HostedPool.Bits() {
		return errors.New("agent subnet must belong to the hosted pool")
	}
	for _, prefix := range p.Protected {
		if !prefix.IsValid() || !prefix.Addr().Is4() {
			return errors.New("protected prefixes must use IPv4")
		}
	}
	for _, endpoint := range p.Allow {
		if !endpoint.Address.Is4() || !endpoint.Address.IsGlobalUnicast() || endpoint.Address.IsLoopback() || endpoint.Address.IsLinkLocalUnicast() || endpoint.Port == 0 || (endpoint.Protocol != "tcp" && endpoint.Protocol != "udp") {
			return errors.New("invalid LAN exception")
		}
		if p.HostedPool.Contains(endpoint.Address) {
			return errors.New("LAN exceptions cannot grant access to another Agent")
		}
		for _, prefix := range p.Protected {
			if prefix.Contains(endpoint.Address) {
				return errors.New("LAN exception overlaps platform protection")
			}
		}
	}
	return nil
}
func (p Policy) chains() (string, string, string) {
	suffix := strings.TrimPrefix(p.Bridge, "jx")
	return "JX-" + suffix, "JXI-" + suffix, "JXR-" + suffix
}

func (p Policy) rules(hosts []netip.Addr) ([]string, []string, []string, error) {
	if err := p.validate(); err != nil {
		return nil, nil, nil, err
	}
	control := fmt.Sprintf("-d %s -p tcp --dport %d -j ACCEPT", p.Control.Addr(), p.Control.Port())
	input := []string{"! -s " + p.Subnet + " -j DROP", control, "-j DROP"}
	forward := []string{"! -s " + p.Subnet + " -j DROP", control}
	for _, address := range hosts {
		if address.Is4() {
			forward = append(forward, "-d "+address.String()+" -j DROP")
		}
	}
	forward = append(forward, "-d "+p.HostedPool.String()+" -j DROP")
	for _, prefix := range p.Protected {
		forward = append(forward, "-d "+prefix.String()+" -j DROP")
	}
	// Metadata/link-local and loopback never become ordinary LAN exceptions.
	for _, prefix := range []string{"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4"} {
		forward = append(forward, "-d "+prefix+" -j DROP")
	}
	for _, endpoint := range p.Allow {
		forward = append(forward, fmt.Sprintf("-d %s -p %s --dport %d -j ACCEPT", endpoint.Address, endpoint.Protocol, endpoint.Port))
	}
	for _, prefix := range blockedNetworks {
		forward = append(forward, "-d "+prefix+" -j DROP")
	}
	forward = append(forward, "-j ACCEPT")
	return forward, input, []string{"-m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT", "-j DROP"}, nil
}

func firewallCommand(ctx context.Context, input string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("hosted firewall %s: %w: %.4096s", name, err, output)
	}
	return nil
}

// Apply installs complete chains before attaching them to traffic. A failure
// leaves the bridge blocked. The caller must not start a guest until it returns.
func (f *Firewall) Apply(ctx context.Context, p Policy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := p.validate(); err != nil {
		return err
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	var hosts []netip.Addr
	for _, value := range addresses {
		prefix, err := netip.ParsePrefix(value.String())
		if err == nil {
			hosts = append(hosts, prefix.Addr().Unmap())
		}
	}
	forward, input, reverse, err := p.rules(hosts)
	if err != nil {
		return err
	}
	// No default-policy change and no mutation of Docker's own chains.
	if err := firewallCommand(ctx, "", "iptables", "-w", "5", "-S", "DOCKER-USER"); err != nil {
		return errors.New("hosted execution requires Docker's iptables backend and DOCKER-USER")
	}
	if err := f.block(ctx, p); err != nil {
		return err
	}
	out, in, back := p.chains()
	var script strings.Builder
	script.WriteString("*filter\n")
	for _, chain := range []string{out, in, back} {
		fmt.Fprintf(&script, ":%s - [0:0]\n-F %s\n", chain, chain)
	}
	for i, rules := range [][]string{forward, input, reverse} {
		chain := []string{out, in, back}[i]
		for _, rule := range rules {
			fmt.Fprintf(&script, "-A %s %s\n", chain, rule)
		}
	}
	script.WriteString("COMMIT\n")
	if err := firewallCommand(ctx, script.String(), "iptables-restore", "--wait", "5", "--noflush"); err != nil {
		return err
	}
	for _, jump := range [][]string{{"DOCKER-USER", "-i", p.Bridge, "-j", out}, {"INPUT", "-i", p.Bridge, "-j", in}, {"DOCKER-USER", "-o", p.Bridge, "-j", back}} {
		if firewallCommand(ctx, "", "iptables", append([]string{"-w", "5", "-C"}, jump...)...) != nil {
			args := append([]string{"-w", "5", "-I", jump[0], "2"}, jump[1:]...)
			if err := firewallCommand(ctx, "", "iptables", args...); err != nil {
				return err
			}
		}
	}
	// IPv6 stays blocked even if a process creates a link-local address.
	for _, jump := range [][]string{{"INPUT", "-i", p.Bridge, "-j", "DROP"}, {"FORWARD", "-i", p.Bridge, "-j", "DROP"}, {"FORWARD", "-o", p.Bridge, "-j", "DROP"}} {
		if firewallCommand(ctx, "", "ip6tables", append([]string{"-w", "5", "-C"}, jump...)...) != nil {
			if err := firewallCommand(ctx, "", "ip6tables", append([]string{"-w", "5", "-I", jump[0], "1"}, jump[1:]...)...); err != nil {
				return err
			}
		}
	}
	for _, chain := range []string{"INPUT", "DOCKER-USER"} {
		if err := firewallCommand(ctx, "", "iptables", "-w", "5", "-D", chain, "-i", p.Bridge, "-j", "DROP"); err != nil {
			return err
		}
	}
	return nil
}
func (f *Firewall) block(ctx context.Context, p Policy) error {
	for _, chain := range []string{"INPUT", "DOCKER-USER"} {
		args := []string{"-w", "5", "-C", chain, "-i", p.Bridge, "-j", "DROP"}
		if firewallCommand(ctx, "", "iptables", args...) != nil {
			if err := firewallCommand(ctx, "", "iptables", "-w", "5", "-I", chain, "1", "-i", p.Bridge, "-j", "DROP"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Remove is called only after the container and its bridge are gone.
func (f *Firewall) Remove(ctx context.Context, p Policy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := p.validate(); err != nil {
		return err
	}
	out, in, back := p.chains()
	for _, entry := range []struct {
		binary string
		rule   []string
	}{
		{"iptables", []string{"DOCKER-USER", "-i", p.Bridge, "-j", out}},
		{"iptables", []string{"INPUT", "-i", p.Bridge, "-j", in}},
		{"iptables", []string{"DOCKER-USER", "-o", p.Bridge, "-j", back}},
		{"iptables", []string{"INPUT", "-i", p.Bridge, "-j", "DROP"}},
		{"iptables", []string{"DOCKER-USER", "-i", p.Bridge, "-j", "DROP"}},
		{"ip6tables", []string{"INPUT", "-i", p.Bridge, "-j", "DROP"}},
		{"ip6tables", []string{"FORWARD", "-i", p.Bridge, "-j", "DROP"}},
		{"ip6tables", []string{"FORWARD", "-o", p.Bridge, "-j", "DROP"}},
	} {
		if firewallCommand(ctx, "", entry.binary, append([]string{"-w", "5", "-C"}, entry.rule...)...) == nil {
			if err := firewallCommand(ctx, "", entry.binary, append([]string{"-w", "5", "-D"}, entry.rule...)...); err != nil {
				return err
			}
		}
	}
	for _, chain := range []string{out, in, back} {
		if firewallCommand(ctx, "", "iptables", "-w", "5", "-S", chain) == nil {
			if err := firewallCommand(ctx, "", "iptables", "-w", "5", "-F", chain); err != nil {
				return err
			}
			if err := firewallCommand(ctx, "", "iptables", "-w", "5", "-X", chain); err != nil {
				return err
			}
		}
	}
	return nil
}
