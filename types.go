package e2b

import (
	"encoding/json"
	"time"
)

const (
	// AllTraffic is the Python SDK's ALL_TRAFFIC sentinel.
	AllTraffic = "0.0.0.0/0"
)

// SandboxState describes a sandbox lifecycle state returned by the API.
type SandboxState string

const (
	SandboxStateRunning SandboxState = "running"
	SandboxStatePaused  SandboxState = "paused"
	SandboxStateKilling SandboxState = "killing"
	SandboxStateKilled  SandboxState = "killed"
)

// SandboxLifecycle controls post-timeout behavior.
type SandboxLifecycle struct {
	OnTimeout  string `json:"on_timeout"`
	AutoResume bool   `json:"auto_resume,omitempty"`
}

// SandboxInfoLifecycle is lifecycle information returned by the API.
type SandboxInfoLifecycle struct {
	OnTimeout  string `json:"on_timeout"`
	AutoResume bool   `json:"auto_resume"`
}

// SandboxNetworkTransform describes egress request transforms for a network
// rule. FC header value replacements use the sandbox-gateway E2B carrier
// "fc.sandbox.network.header-value-replacements" inside Headers.
type SandboxNetworkTransform struct {
	Headers map[string]string `json:"headers,omitempty"`
}

// SandboxNetworkRule describes a per-host egress rule.
type SandboxNetworkRule struct {
	Transform *SandboxNetworkTransform `json:"transform,omitempty"`
}

// SandboxNetworkRules maps exact destination hostnames to ordered rules. A host
// registered here is not automatically allowed by the egress policy.
type SandboxNetworkRules map[string][]SandboxNetworkRule

// SandboxNetworkOpts is used when creating sandboxes.
type SandboxNetworkOpts struct {
	AllowOut           []string            `json:"allowOut,omitempty"`
	DenyOut            []string            `json:"denyOut,omitempty"`
	Rules              SandboxNetworkRules `json:"rules,omitempty"`
	AllowPublicTraffic *bool               `json:"allow_public_traffic,omitempty"`
	MaskRequestHost    string              `json:"mask_request_host,omitempty"`
}

// SandboxNetworkUpdate atomically replaces the mutable sandbox network
// configuration. The control plane clears AllowOut, DenyOut, or Rules when the
// corresponding field is omitted, so callers must resend values they want to
// preserve.
type SandboxNetworkUpdate struct {
	AllowOut            []string            `json:"allowOut,omitempty"`
	DenyOut             []string            `json:"denyOut,omitempty"`
	Rules               SandboxNetworkRules `json:"rules,omitempty"`
	AllowInternetAccess *bool               `json:"allow_internet_access,omitempty"`
}

// SandboxNetworkInfo is network configuration returned by sandbox info.
type SandboxNetworkInfo struct {
	AllowOut           []string            `json:"allowOut,omitempty"`
	DenyOut            []string            `json:"denyOut,omitempty"`
	Rules              SandboxNetworkRules `json:"rules,omitempty"`
	AllowPublicTraffic *bool               `json:"allow_public_traffic,omitempty"`
	MaskRequestHost    string              `json:"mask_request_host,omitempty"`
}

// SandboxVolumeMount maps a team volume into a sandbox.
type SandboxVolumeMount struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// SandboxInfo contains sandbox metadata returned by the API.
type SandboxInfo struct {
	SandboxID           string                 `json:"sandboxID"`
	SandboxDomain       string                 `json:"domain,omitempty"`
	TemplateID          string                 `json:"templateID"`
	Name                string                 `json:"alias,omitempty"`
	Metadata            map[string]string      `json:"metadata,omitempty"`
	StartedAt           time.Time              `json:"startedAt"`
	EndAt               time.Time              `json:"endAt"`
	State               SandboxState           `json:"state"`
	CPUCount            int                    `json:"cpuCount"`
	MemoryMB            int                    `json:"memoryMB"`
	DiskSizeMB          int                    `json:"diskSizeMB,omitempty"`
	EnvdVersion         string                 `json:"envdVersion"`
	EnvdAccessToken     string                 `json:"envdAccessToken,omitempty"`
	AllowInternetAccess *bool                  `json:"allowInternetAccess,omitempty"`
	Network             *SandboxNetworkInfo    `json:"network,omitempty"`
	Lifecycle           *SandboxInfoLifecycle  `json:"lifecycle,omitempty"`
	VolumeMounts        []SandboxVolumeMount   `json:"volumeMounts,omitempty"`
	Additional          map[string]interface{} `json:"-"`
}

// SandboxQuery filters sandbox listing.
type SandboxQuery struct {
	Metadata map[string]string
	State    []SandboxState
}

// SandboxMetrics contains CPU, memory, and disk usage.
type SandboxMetrics struct {
	CPUCount   int       `json:"cpuCount"`
	CPUUsedPct float64   `json:"cpuUsedPct"`
	DiskTotal  int64     `json:"diskTotal"`
	DiskUsed   int64     `json:"diskUsed"`
	MemTotal   int64     `json:"memTotal"`
	MemUsed    int64     `json:"memUsed"`
	MemCache   int64     `json:"memCache"`
	Timestamp  time.Time `json:"timestamp"`
}

// SnapshotInfo contains snapshot identifiers returned by the API.
type SnapshotInfo struct {
	SnapshotID string   `json:"snapshotID"`
	Names      []string `json:"names,omitempty"`
}

// FileType represents a sandbox or volume filesystem object type.
type FileType string

const (
	FileTypeFile FileType = "file"
	FileTypeDir  FileType = "dir"
)

// WriteInfo is returned by file upload operations.
type WriteInfo struct {
	Name     string            `json:"name"`
	Type     FileType          `json:"type"`
	Path     string            `json:"path"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (w *WriteInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name     string            `json:"name"`
		Type     any               `json:"type"`
		Path     string            `json:"path"`
		Metadata map[string]string `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fileType, _ := mapProtoFileType(raw.Type)
	w.Name = raw.Name
	w.Type = fileType
	w.Path = raw.Path
	w.Metadata = raw.Metadata
	return nil
}

// EntryInfo contains filesystem metadata.
type EntryInfo struct {
	WriteInfo
	Size          int64     `json:"size"`
	Mode          uint32    `json:"mode"`
	Permissions   string    `json:"permissions"`
	Owner         string    `json:"owner"`
	Group         string    `json:"group"`
	ModifiedTime  time.Time `json:"modifiedTime"`
	SymlinkTarget *string   `json:"symlinkTarget,omitempty"`
}

// FilesystemEventType is a sandbox filesystem watch event.
type FilesystemEventType string

const (
	FilesystemEventCreate FilesystemEventType = "create"
	FilesystemEventWrite  FilesystemEventType = "write"
	FilesystemEventRemove FilesystemEventType = "remove"
	FilesystemEventRename FilesystemEventType = "rename"
	FilesystemEventChmod  FilesystemEventType = "chmod"
)

// FilesystemEvent is returned by WatchHandle.
type FilesystemEvent struct {
	Name  string              `json:"name"`
	Type  FilesystemEventType `json:"type"`
	Entry *EntryInfo          `json:"entry,omitempty"`
}

// ProcessInfo describes a running command or PTY session.
type ProcessInfo struct {
	PID  int               `json:"pid"`
	Tag  string            `json:"tag,omitempty"`
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args,omitempty"`
	Envs map[string]string `json:"envs,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
}

// PtySize is a pseudo-terminal size.
type PtySize struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// CommandResult is the final command result.
type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// PtyOutput is raw PTY output.
type PtyOutput []byte

// GitFileStatus mirrors the Python SDK git status entry.
type GitFileStatus struct {
	Path string `json:"path"`
	X    string `json:"x,omitempty"`
	Y    string `json:"y,omitempty"`
}

// GitStatus contains parsed git status output.
type GitStatus struct {
	Branch    string          `json:"branch,omitempty"`
	Upstream  string          `json:"upstream,omitempty"`
	Ahead     int             `json:"ahead,omitempty"`
	Behind    int             `json:"behind,omitempty"`
	Files     []GitFileStatus `json:"files,omitempty"`
	Raw       string          `json:"raw,omitempty"`
	IsClean   bool            `json:"isClean"`
	Conflicts []GitFileStatus `json:"conflicts,omitempty"`
}

// GitBranches contains local/current branch information.
type GitBranches struct {
	Current string   `json:"current,omitempty"`
	Local   []string `json:"local,omitempty"`
	Remote  []string `json:"remote,omitempty"`
	Raw     string   `json:"raw,omitempty"`
}

// GitResetMode is a git reset mode.
type GitResetMode string

const (
	GitResetSoft  GitResetMode = "soft"
	GitResetMixed GitResetMode = "mixed"
	GitResetHard  GitResetMode = "hard"
)
