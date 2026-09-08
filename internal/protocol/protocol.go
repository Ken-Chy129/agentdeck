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

// ---- sync ----

type SyncRequest struct {
	Inventory   *Inventory   `json:"inventory,omitempty"`
	LocalSkills []LocalSkill `json:"local_skills"`
	CLIVersion  string       `json:"cli_version"`
}

type DesiredSkill struct {
	Name      string `json:"name"`
	VersionID int64  `json:"version_id"`
	Version   int    `json:"version"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type Job struct {
	ID      int64           `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type SyncResponse struct {
	MachineID   string         `json:"machine_id"`
	MachineName string         `json:"machine_name"`
	Skills      []DesiredSkill `json:"skills"`
	Jobs        []Job          `json:"jobs"`
}

type SkillResult struct {
	Name   string `json:"name"`
	Action string `json:"action"` // installed | updated | removed | unchanged | failed
	From   int    `json:"from,omitempty"`
	To     int    `json:"to,omitempty"`
	Error  string `json:"error,omitempty"`
	Backup string `json:"backup,omitempty"`
}

type JobResult struct {
	ID     int64  `json:"id"`
	Status string `json:"status"` // done | failed
	Output string `json:"output"`
}

type SyncReport struct {
	Skills   []SkillResult `json:"skills"`
	Jobs     []JobResult   `json:"jobs"`
	Duration string        `json:"duration"`
	Error    string        `json:"error,omitempty"`
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
)
