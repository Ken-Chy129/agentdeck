// Package protocol defines the JSON shapes exchanged between the sync CLI and the server.
package protocol

import "encoding/json"

// ---- inventory (client -> server) ----

type CLITool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
	Source  string `json:"source"` // npm-global | brew | binary | unknown
	Package string `json:"package,omitempty"`
	// Upgrade is the exact command that upgrades this tool on this machine.
	// Computed locally because only the machine knows its real layout (npm
	// prefix, nvm dir, Anthropic's native installer, codex standalone, ...).
	Upgrade string `json:"upgrade,omitempty"`
	// Shadowed lists other copies of this binary further down $PATH. Duplicate
	// installs are a common cause of "I upgraded it but the old version keeps
	// running".
	Shadowed []string `json:"shadowed,omitempty"`
}

type Runtime struct {
	Name    string `json:"name"` // node, go, python3, uv, ...
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
}

type Inventory struct {
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	Hostname  string    `json:"hostname"`
	Runtimes  []Runtime `json:"runtimes"`
	NpmGlobal []CLITool `json:"npm_global"`
	Brew      []CLITool `json:"brew"`
	Tools     []CLITool `json:"tools"` // resolved agent CLIs (claude, codex, ...)
	Agents    []Agent   `json:"agents"`
	Errors    []string  `json:"errors,omitempty"`
}

// Agent describes a coding-agent install found on the machine.
type Agent struct {
	Name      string `json:"name"` // claude | codex | gemini | cursor
	SkillsDir string `json:"skills_dir"`
	Exists    bool   `json:"exists"`
	SkillN    int    `json:"skill_count"`
}

// LocalSkill is one skill directory observed on the machine.
type LocalSkill struct {
	Name    string `json:"name"`
	Digest  string `json:"digest"`
	Managed bool   `json:"managed"` // present in the local lockfile
	Version int    `json:"version,omitempty"`
	Size    int64  `json:"size"`
}

// AppliedState is what the machine currently has for each managed resource.
type AppliedState struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// ---- sync ----

type SyncRequest struct {
	Inventory   *Inventory     `json:"inventory,omitempty"`
	Snapshot    *Snapshot      `json:"snapshot,omitempty"`
	LocalSkills []LocalSkill   `json:"local_skills"`
	Applied     []AppliedState `json:"applied"`
	CLIVersion  string         `json:"cli_version"`
}

// DesiredResource is one resource the machine should have. Content delivery:
//
//	skill  -> fetched via /archive by version_id (tar.gz)
//	env    -> Value inline (already resolved for this machine)
//	config -> Content inline (profile text) + Override inline (machine overrides), both same format
type DesiredResource struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	VersionID int64  `json:"version_id"`
	Version   int    `json:"version"`
	Digest    string `json:"digest"` // effective digest incl. override
	Size      int64  `json:"size,omitempty"`
	Value     string `json:"value,omitempty"`    // env
	Secret    bool   `json:"secret,omitempty"`   // env
	Content   string `json:"content,omitempty"`  // config profile text
	Override  string `json:"override,omitempty"` // config machine override text
	Tool      string `json:"tool,omitempty"`     // config: claude | codex | ...
	Path      string `json:"path,omitempty"`     // config: target file (~ allowed)
	Format    string `json:"format,omitempty"`   // config: json | toml | yaml
}

type Job struct {
	ID      int64           `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type SyncResponse struct {
	MachineID   string            `json:"machine_id"`
	MachineName string            `json:"machine_name"`
	Resources   []DesiredResource `json:"resources"`
	Jobs        []Job             `json:"jobs"`
	// Import requests: server asks the machine to upload the real value of these exports.
	ImportEnv []string `json:"import_env,omitempty"`
}

type ResourceResult struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"` // applied | unchanged | removed | failed
	Digest string `json:"digest,omitempty"`
	Error  string `json:"error,omitempty"`
	Backup string `json:"backup,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type JobResult struct {
	ID     int64  `json:"id"`
	Status string `json:"status"` // done | failed
	Output string `json:"output"`
}

// PollResponse answers the agent's long poll with work to do right now.
type PollResponse struct {
	Jobs []Job `json:"jobs"`
}

type SyncReport struct {
	Resources []ResourceResult  `json:"resources"`
	Jobs      []JobResult       `json:"jobs"`
	Duration  string            `json:"duration"`
	Error     string            `json:"error,omitempty"`
	Imported  map[string]string `json:"imported,omitempty"` // env name -> real value (TLS only)
}

// ---- enroll ----

type EnrollRequest struct {
	EnrollToken string `json:"enroll_token"`
	Name        string `json:"name"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Hostname    string `json:"hostname"`
}

type EnrollResponse struct {
	MachineID    string `json:"machine_id"`
	MachineToken string `json:"machine_token"`
	Name         string `json:"name"`
}

// Job types accepted by the client. Anything else is rejected locally.
const (
	JobNpmUpgrade  = "npm_upgrade"  // {"package":"@openai/codex","version":"latest"}
	JobBrewUpgrade = "brew_upgrade" // {"formula":"gh"}
	JobEcho        = "echo"         // {"message":"..."} smoke test
	JobShell       = "shell"        // {"cmd":"npm i -g x@latest","cwd":"~","timeout_sec":600}
	// JobSync asks the machine to run a full reconcile right now instead of
	// waiting for its next scheduled sync. Payload is empty.
	JobSync = "sync"
)

// ---- config collection (client -> server) ----

type ConfigFile struct {
	Tool      string `json:"tool"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	Size      int64  `json:"size"`
	ModTime   string `json:"mod_time"`
	Digest    string `json:"digest"`  // of the real file, for change detection
	Content   string `json:"content"` // redacted
	Truncated bool   `json:"truncated,omitempty"`
}

type EnvExport struct {
	Name        string `json:"name"`
	Value       string `json:"value"` // redacted if Kind == secret
	Kind        string `json:"kind"`  // plain | secret | append
	File        string `json:"file"`
	Line        int    `json:"line"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type Snapshot struct {
	Configs []ConfigFile `json:"configs"`
	Exports []EnvExport  `json:"exports"`
}
