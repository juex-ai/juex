package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

func (m *Manager) spawn(intent launchIntent) error {
	id := intent.Runtime.ServiceID
	path := filepath.Join(serviceendpoint.StateDir(m.home, id), "service.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer stdin.Close()
	command := intent.Definition.Command
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = serviceendpoint.StateDir(m.home, id)
	cmd.Env = serviceEnvironment(os.Environ(), map[string]string{"JUEX_HOME": m.home, "JUEX_FLEET_ID": m.fleet, "JUEX_SERVICE_ID": id, "JUEX_SERVICE_INSTANCE": intent.Runtime.InstanceID})
	cmd.Stdin = stdin
	cmd.Stdout = file
	cmd.Stderr = file
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
func serviceEnvironment(environment []string, values map[string]string) []string {
	result := make([]string, 0, len(environment)+len(values))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		found := false
		for key := range values {
			if strings.EqualFold(name, key) {
				found = true
				break
			}
		}
		if !found {
			result = append(result, item)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
