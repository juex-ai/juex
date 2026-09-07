package agentstate

// RuntimePaths separates workspace-local configuration from identity-owned
// runtime state.
type RuntimePaths struct {
	WorkDir               string
	JuexDir               string
	StateDir              string
	MediaDir              string
	ThreadsDir            string
	ThreadIndexPath       string
	WorkspaceConfigPath   string
	DefaultHomeConfigPath string
	HomeConfigPath        string
	AgentConfigPath       string
}
