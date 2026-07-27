// Command env-mcp is a minimal MCP stdio server used by Fleet environment
// propagation tests. It records one environment value in the active workspace
// before completing the MCP handshake.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

func main() {
	workDir := os.Getenv("WORKDIR")
	if workDir == "" {
		fmt.Fprintln(os.Stderr, "WORKDIR is empty")
		os.Exit(1)
	}
	if err := os.WriteFile(
		filepath.Join(workDir, "fleet-env-seen"),
		[]byte(os.Getenv("FLEET_ENV_SENTINEL")),
		0o600,
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo": map[string]any{
					"name":    "env-mcp",
					"version": "test",
				},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{}}
		default:
			result = map[string]any{}
		}
		if err := encoder.Encode(response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
