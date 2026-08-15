package agentwaker

import "encoding/json"

const (
	adapterVersion          = "0.1.0"
	profileSchemaVersion    = "2.1"
	registrySchemaVersion   = "1.0"
	capabilitySchemaVersion = "1.0"
	bindingSchemaVersion    = "1.0"
	automationSchemaVersion = "1.0"
)

type config struct {
	RootPath string `json:"root_path"`
}

type registry struct {
	SchemaVersion string               `yaml:"schema_version"`
	Capabilities  []registryCapability `yaml:"capabilities"`
}

type registryCapability struct {
	ID       string `yaml:"id"`
	Version  string `yaml:"version"`
	Manifest string `yaml:"manifest"`
}

type capabilityManifest struct {
	SchemaVersion string               `yaml:"schema_version"`
	ID            string               `yaml:"id"`
	Name          string               `yaml:"name"`
	Version       string               `yaml:"version"`
	Description   string               `yaml:"description"`
	Entrypoint    string               `yaml:"entrypoint"`
	Profiles      []capabilityProfile  `yaml:"profiles"`
	Adapters      []capabilityAdapter  `yaml:"adapters"`
	Contracts     capabilityContracts  `yaml:"contracts"`
	Requires      capabilityRequires   `yaml:"requires"`
	Permissions   capabilityPermission `yaml:"permissions"`
}

type capabilityProfile struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
}

type capabilityContracts struct {
	InputSchema  string `yaml:"input_schema"`
	OutputSchema string `yaml:"output_schema"`
}

type capabilityAdapter struct {
	ID          string `yaml:"id"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

type capabilityRequires struct {
	Environment []string `yaml:"environment"`
	MCP         []string `yaml:"mcp"`
}

type capabilityPermission struct {
	DefaultMode            string `yaml:"default_mode"`
	SupportsAccountActions bool   `yaml:"supports_account_actions"`
}

type profile struct {
	SchemaVersion string            `yaml:"schema_version"`
	ID            string            `yaml:"id"`
	DisplayName   string            `yaml:"display_name"`
	RoleType      string            `yaml:"role_type"`
	Title         string            `yaml:"title"`
	Version       string            `yaml:"version"`
	Lifecycle     string            `yaml:"lifecycle"`
	Mission       string            `yaml:"mission"`
	Skills        profileSkills     `yaml:"skills"`
	Generation    profileGeneration `yaml:"generation"`
}

type profileGeneration struct {
	CardTitleZH   string `yaml:"card_title_zh"`
	CardMissionZH string `yaml:"card_mission_zh"`
}

type profileSkills struct {
	Directory      string             `yaml:"directory"`
	MetaEntrypoint string             `yaml:"meta_entrypoint"`
	EnvExample     string             `yaml:"env_example"`
	Items          []profileSkillItem `yaml:"items"`
}

type profileSkillItem struct {
	ID         string `yaml:"id"`
	Name       string `yaml:"name"`
	UseWhen    string `yaml:"use_when"`
	Entrypoint string `yaml:"entrypoint"`
	Status     string `yaml:"status"`
}

type roleCapabilities struct {
	SchemaVersion string                  `yaml:"schema_version"`
	Role          string                  `yaml:"role"`
	Capabilities  []roleCapabilityBinding `yaml:"capabilities"`
}

type roleCapabilityBinding struct {
	ID          string                   `yaml:"id"`
	Version     string                   `yaml:"version"`
	Required    bool                     `yaml:"required"`
	UsedBy      []roleCapabilityUse      `yaml:"used_by"`
	Permissions roleCapabilityPermission `yaml:"permissions"`
	Fallback    roleCapabilityFallback   `yaml:"fallback"`
}

type roleCapabilityUse struct {
	Skill   string `yaml:"skill"`
	Profile string `yaml:"profile"`
}

type roleCapabilityPermission struct {
	Mode           string `yaml:"mode"`
	AccountActions bool   `yaml:"account_actions"`
}

type roleCapabilityFallback struct {
	Behavior string `yaml:"behavior"`
	Message  string `yaml:"message"`
}

type mcpConfig struct {
	Servers map[string]json.RawMessage `json:"mcpServers"`
}

type automationManifest struct {
	SchemaVersion string             `yaml:"schema_version"`
	RoleID        string             `yaml:"role_id"`
	Automations   []sourceAutomation `yaml:"automations"`
}

type sourceAutomation struct {
	ID         string               `yaml:"id"`
	Title      string               `yaml:"title"`
	PromptFile string               `yaml:"prompt_file"`
	Execution  automationExecution  `yaml:"execution"`
	Schedule   automationSchedule   `yaml:"schedule"`
	Sync       automationSync       `yaml:"sync"`
	Governance automationGovernance `yaml:"governance"`
}

type automationExecution struct {
	Mode               string `yaml:"mode"`
	IssueTitleTemplate string `yaml:"issue_title_template"`
}

type automationSchedule struct {
	Kind           string `yaml:"kind"`
	Expression     string `yaml:"expression"`
	Timezone       string `yaml:"timezone"`
	InitialEnabled bool   `yaml:"initial_enabled"`
	Label          string `yaml:"label"`
}

type automationSync struct {
	Content    string `yaml:"content"`
	Schedule   string `yaml:"schedule"`
	Activation string `yaml:"activation"`
	Missing    string `yaml:"missing"`
}

type automationGovernance struct {
	ExternalWrites string `yaml:"external_writes"`
}
