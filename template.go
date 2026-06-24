package e2b

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TemplateBuildStatus is the status of a template build.
type TemplateBuildStatus string

const (
	TemplateBuildStatusBuilding TemplateBuildStatus = "building"
	TemplateBuildStatusWaiting  TemplateBuildStatus = "waiting"
	TemplateBuildStatusReady    TemplateBuildStatus = "ready"
	TemplateBuildStatusError    TemplateBuildStatus = "error"
)

// TemplateInstructionType is a template build instruction type.
type TemplateInstructionType string

const (
	InstructionCopy    TemplateInstructionType = "COPY"
	InstructionEnv     TemplateInstructionType = "ENV"
	InstructionRun     TemplateInstructionType = "RUN"
	InstructionWorkdir TemplateInstructionType = "WORKDIR"
	InstructionUser    TemplateInstructionType = "USER"
)

// TemplateInstruction is one build instruction.
type TemplateInstruction struct {
	Type      TemplateInstructionType `json:"type"`
	Args      []string                `json:"args"`
	Force     bool                    `json:"force,omitempty"`
	User      string                  `json:"user,omitempty"`
	Mode      *int                    `json:"mode,omitempty"`
	FilesHash string                  `json:"filesHash,omitempty"`
}

// Template describes an E2B template build.
type Template struct {
	Image        string                `json:"fromImage,omitempty"`
	FromTemplate string                `json:"fromTemplate,omitempty"`
	StartCmd     string                `json:"startCmd,omitempty"`
	ReadyCmd     string                `json:"readyCmd,omitempty"`
	Steps        []TemplateInstruction `json:"steps"`
	Force        bool                  `json:"force"`
}

// NewTemplate creates an empty template builder.
func NewTemplate() *Template {
	return &Template{Steps: []TemplateInstruction{}}
}

func (t *Template) FromDockerImage(image string) *Template {
	t.Image = image
	return t
}

// FromImage sets a Docker image as the template base image.
func (t *Template) FromImage(image string) *Template {
	return t.FromDockerImage(image)
}

func (t *Template) FromBaseTemplate(template string) *Template {
	t.FromTemplate = template
	return t
}

func (t *Template) RunCmd(cmd string) *Template {
	t.Steps = append(t.Steps, TemplateInstruction{Type: InstructionRun, Args: []string{cmd}})
	return t
}

func (t *Template) SetEnv(key, value string) *Template {
	t.Steps = append(t.Steps, TemplateInstruction{Type: InstructionEnv, Args: []string{key, value}})
	return t
}

func (t *Template) Workdir(path string) *Template {
	t.Steps = append(t.Steps, TemplateInstruction{Type: InstructionWorkdir, Args: []string{path}})
	return t
}

func (t *Template) User(user string) *Template {
	t.Steps = append(t.Steps, TemplateInstruction{Type: InstructionUser, Args: []string{user}})
	return t
}

func (t *Template) Copy(src, dest string) *Template {
	t.Steps = append(t.Steps, TemplateInstruction{Type: InstructionCopy, Args: []string{src, dest}})
	return t
}

func (t *Template) WithStartCmd(cmd string) *Template {
	t.StartCmd = cmd
	return t
}

func (t *Template) WithReadyCmd(cmd string) *Template {
	t.ReadyCmd = cmd
	return t
}

// BuildInfo identifies a requested template build.
type BuildInfo struct {
	TemplateID string   `json:"templateID"`
	BuildID    string   `json:"buildID"`
	Name       string   `json:"name"`
	Alias      string   `json:"alias"`
	Tags       []string `json:"tags,omitempty"`
}

// LogEntry is a template build log entry.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// BuildStatusReason describes why a build failed.
type BuildStatusReason struct {
	Message    string     `json:"message"`
	Step       string     `json:"step,omitempty"`
	LogEntries []LogEntry `json:"logEntries,omitempty"`
}

// TemplateBuildStatusResponse is returned by GetBuildStatus.
type TemplateBuildStatusResponse struct {
	BuildID    string              `json:"buildID"`
	TemplateID string              `json:"templateID"`
	Status     TemplateBuildStatus `json:"status"`
	LogEntries []LogEntry          `json:"logEntries"`
	Logs       []string            `json:"logs"`
	Reason     *BuildStatusReason  `json:"reason,omitempty"`
}

// TemplateTagInfo contains assigned tags.
type TemplateTagInfo struct {
	BuildID string   `json:"buildID"`
	Tags    []string `json:"tags"`
}

// TemplateTag is one template tag.
type TemplateTag struct {
	Tag       string    `json:"tag"`
	BuildID   string    `json:"buildID"`
	CreatedAt time.Time `json:"createdAt"`
}

// TemplateInfo contains metadata for a template returned by the API.
type TemplateInfo struct {
	TemplateID    string              `json:"templateID"`
	Aliases       []string            `json:"aliases"`
	Names         []string            `json:"names"`
	Public        bool                `json:"public"`
	BuildID       string              `json:"buildID,omitempty"`
	BuildStatus   TemplateBuildStatus `json:"buildStatus,omitempty"`
	BuildCount    int                 `json:"buildCount,omitempty"`
	SpawnCount    int                 `json:"spawnCount,omitempty"`
	CPUCount      int                 `json:"cpuCount,omitempty"`
	MemoryMB      int                 `json:"memoryMB,omitempty"`
	DiskSizeMB    int                 `json:"diskSizeMB,omitempty"`
	EnvdVersion   string              `json:"envdVersion,omitempty"`
	CreatedAt     time.Time           `json:"createdAt,omitempty"`
	UpdatedAt     time.Time           `json:"updatedAt,omitempty"`
	LastSpawnedAt *time.Time          `json:"lastSpawnedAt,omitempty"`
}

// TemplateBuild contains one build listed under a template.
type TemplateBuild struct {
	BuildID     string              `json:"buildID"`
	Status      TemplateBuildStatus `json:"status"`
	CPUCount    int                 `json:"cpuCount"`
	MemoryMB    int                 `json:"memoryMB"`
	DiskSizeMB  int                 `json:"diskSizeMB,omitempty"`
	EnvdVersion string              `json:"envdVersion,omitempty"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
	FinishedAt  *time.Time          `json:"finishedAt,omitempty"`
}

// TemplateWithBuilds contains template metadata and a page of builds.
type TemplateWithBuilds struct {
	TemplateID    string          `json:"templateID"`
	Aliases       []string        `json:"aliases"`
	Names         []string        `json:"names"`
	Public        bool            `json:"public"`
	SpawnCount    int             `json:"spawnCount"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	LastSpawnedAt *time.Time      `json:"lastSpawnedAt,omitempty"`
	Builds        []TemplateBuild `json:"builds"`
	NextToken     string          `json:"-"`
	HasNext       bool            `json:"-"`
}

type templateBuildOptions struct {
	tags       []string
	cpuCount   int
	memoryMB   int
	skipCache  bool
	pollPeriod time.Duration
}

// TemplateBuildOption configures template builds.
type TemplateBuildOption func(*templateBuildOptions)

func WithTemplateTags(tags ...string) TemplateBuildOption {
	return func(o *templateBuildOptions) { o.tags = append([]string{}, tags...) }
}

func WithTemplateCPUCount(cpu int) TemplateBuildOption {
	return func(o *templateBuildOptions) { o.cpuCount = cpu }
}

func WithTemplateMemoryMB(memory int) TemplateBuildOption {
	return func(o *templateBuildOptions) { o.memoryMB = memory }
}

func WithTemplateSkipCache(skip bool) TemplateBuildOption {
	return func(o *templateBuildOptions) { o.skipCache = skip }
}

func WithTemplatePollPeriod(period time.Duration) TemplateBuildOption {
	return func(o *templateBuildOptions) { o.pollPeriod = period }
}

func templateBuildOptionsFrom(opts ...TemplateBuildOption) templateBuildOptions {
	options := templateBuildOptions{cpuCount: 2, memoryMB: 1024, pollPeriod: 200 * time.Millisecond}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

// BuildTemplate builds a template and waits until it finishes.
func (c *Client) BuildTemplate(ctx context.Context, template *Template, name string, opts ...TemplateBuildOption) (BuildInfo, error) {
	info, err := c.BuildTemplateInBackground(ctx, template, name, opts...)
	if err != nil {
		return BuildInfo{}, err
	}
	options := templateBuildOptionsFrom(opts...)
	offset := 0
	for {
		status, err := c.GetBuildStatus(ctx, info.TemplateID, info.BuildID, offset)
		if err != nil {
			return BuildInfo{}, err
		}
		offset += len(status.LogEntries)
		switch status.Status {
		case TemplateBuildStatusReady:
			return info, nil
		case TemplateBuildStatusError:
			if status.Reason != nil && status.Reason.Message != "" {
				return BuildInfo{}, &BuildError{Message: status.Reason.Message}
			}
			return BuildInfo{}, &BuildError{Message: "build failed"}
		}
		select {
		case <-ctx.Done():
			return BuildInfo{}, ctx.Err()
		case <-time.After(options.pollPeriod):
		}
	}
}

// BuildTemplateInBackground requests and triggers a template build.
func (c *Client) BuildTemplateInBackground(ctx context.Context, template *Template, name string, opts ...TemplateBuildOption) (BuildInfo, error) {
	options := templateBuildOptionsFrom(opts...)
	req := map[string]any{
		"name":     name,
		"cpuCount": options.cpuCount,
		"memoryMB": options.memoryMB,
	}
	if len(options.tags) > 0 {
		req["tags"] = options.tags
	}
	var response struct {
		TemplateID string   `json:"templateID"`
		BuildID    string   `json:"buildID"`
		Aliases    []string `json:"aliases"`
		Names      []string `json:"names"`
		Public     bool     `json:"public"`
		Tags       []string `json:"tags"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v3/templates", nil, req, &response, http.StatusAccepted); err != nil {
		return BuildInfo{}, err
	}
	if template != nil {
		body := *template
		body.Steps = make([]TemplateInstruction, len(template.Steps))
		copy(body.Steps, template.Steps)
		if options.skipCache {
			body.Force = true
		}
		if err := c.prepareTemplateFiles(ctx, response.TemplateID, &body); err != nil {
			return BuildInfo{}, &BuildError{Message: c.withTemplateCleanupError(response.TemplateID, err).Error()}
		}
		path := "/v2/templates/" + url.PathEscape(response.TemplateID) + "/builds/" + url.PathEscape(response.BuildID)
		if err := c.doJSON(ctx, http.MethodPost, path, nil, body, nil); err != nil {
			return BuildInfo{}, &BuildError{Message: c.withTemplateCleanupError(response.TemplateID, err).Error()}
		}
	}
	return BuildInfo{
		TemplateID: response.TemplateID,
		BuildID:    response.BuildID,
		Name:       name,
		Alias:      name,
		Tags:       response.Tags,
	}, nil
}

func (c *Client) withTemplateCleanupError(templateID string, cause error) error {
	if err := c.deleteTemplateAfterBuildStartFailure(templateID); err != nil {
		return fmt.Errorf("%w; additionally failed to delete template %s after build start failure: %v", cause, templateID, err)
	}
	return cause
}

func (c *Client) deleteTemplateAfterBuildStartFailure(templateID string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		deleted, err := c.DeleteTemplate(ctx, templateID)
		cancel()
		if err == nil {
			if deleted {
				return nil
			}
			return nil
		}
		lastErr = err
	}
	return lastErr
}

type templateBuildFileUpload struct {
	Present bool   `json:"present"`
	URL     string `json:"url,omitempty"`
}

func (c *Client) prepareTemplateFiles(ctx context.Context, templateID string, template *Template) error {
	if template == nil {
		return nil
	}
	contextPath, err := os.Getwd()
	if err != nil {
		return err
	}
	for i := range template.Steps {
		step := &template.Steps[i]
		if step.Type != InstructionCopy {
			continue
		}
		if len(step.Args) < 2 {
			return fmt.Errorf("COPY requires source and destination arguments")
		}
		src, dest := step.Args[0], step.Args[1]
		hash, err := calculateTemplateCopyHash(src, dest, contextPath)
		if err != nil {
			return err
		}
		step.FilesHash = hash

		upload, err := c.getTemplateFileUpload(ctx, templateID, hash)
		if err != nil {
			return err
		}
		if upload.Present {
			continue
		}
		if upload.URL == "" {
			return fmt.Errorf("template file upload URL is empty for hash %s", hash)
		}
		tarball, err := createTemplateCopyTar(src, contextPath)
		if err != nil {
			return err
		}
		if err := c.uploadTemplateFiles(ctx, upload.URL, tarball); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) getTemplateFileUpload(ctx context.Context, templateID, hash string) (templateBuildFileUpload, error) {
	var response templateBuildFileUpload
	path := "/templates/" + url.PathEscape(templateID) + "/files/" + url.PathEscape(hash)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &response, http.StatusOK, http.StatusCreated); err != nil {
		return templateBuildFileUpload{}, err
	}
	return response, nil
}

func (c *Client) uploadTemplateFiles(ctx context.Context, uploadURL string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/gzip")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("upload template files: %d: %s", res.StatusCode, string(body))
	}
	return nil
}

func calculateTemplateCopyHash(src, dest, contextPath string) (string, error) {
	if err := validateTemplateCopySource(src); err != nil {
		return "", err
	}
	files, err := templateCopyFiles(src, contextPath)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("COPY " + src + " " + dest))
	for _, file := range files {
		rel, err := filepath.Rel(contextPath, file)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(filepath.ToSlash(rel)))
		info, err := os.Lstat(file)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(strconv.FormatUint(uint64(info.Mode()), 10)))
		_, _ = hash.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(file)
			if err != nil {
				return "", err
			}
			_, _ = hash.Write([]byte(target))
			continue
		}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(file)
			if err != nil {
				return "", err
			}
			_, _ = hash.Write(data)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func createTemplateCopyTar(src, contextPath string) ([]byte, error) {
	if err := validateTemplateCopySource(src); err != nil {
		return nil, err
	}
	files, err := templateCopyFiles(src, contextPath)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, file := range files {
		info, err := os.Lstat(file)
		if err != nil {
			return nil, err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(file)
			if err != nil {
				return nil, err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(contextPath, file)
		if err != nil {
			return nil, err
		}
		header.Name = filepath.ToSlash(rel)
		header.ModTime = time.Time{}
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(file)
			if err != nil {
				return nil, err
			}
			_, copyErr := io.Copy(tw, f)
			closeErr := f.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func templateCopyFiles(src, contextPath string) ([]string, error) {
	if err := validateTemplateCopySource(src); err != nil {
		return nil, err
	}
	pattern := filepath.Join(contextPath, filepath.FromSlash(src))
	var matches []string
	var err error
	if strings.ContainsAny(src, "*?[") {
		matches, err = filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
	} else {
		matches = []string{pattern}
	}
	seen := map[string]struct{}{}
	for _, match := range matches {
		if err := ensureTemplateCopyWithinContext(match, contextPath); err != nil {
			return nil, err
		}
		info, err := os.Lstat(match)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if info.IsDir() {
			err = filepath.WalkDir(match, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if err := ensureTemplateCopyWithinContext(path, contextPath); err != nil {
					return err
				}
				seen[path] = struct{}{}
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		seen[match] = struct{}{}
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found in %s", pattern)
	}
	return files, nil
}

func validateTemplateCopySource(src string) error {
	if src == "" {
		return fmt.Errorf("COPY source cannot be empty")
	}
	if filepath.IsAbs(src) {
		return fmt.Errorf("invalid COPY source %q: absolute paths are not allowed", src)
	}
	clean := filepath.Clean(filepath.FromSlash(src))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid COPY source %q: path escapes the build context", src)
	}
	return nil
}

func ensureTemplateCopyWithinContext(path, contextPath string) error {
	rel, err := filepath.Rel(contextPath, path)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("COPY source %q escapes the build context", path)
	}
	return nil
}

// GetBuildStatus returns template build status.
func (c *Client) GetBuildStatus(ctx context.Context, templateID, buildID string, logsOffset int) (TemplateBuildStatusResponse, error) {
	query := url.Values{"logsOffset": []string{strconvItoa(logsOffset)}}
	var response TemplateBuildStatusResponse
	path := "/templates/" + url.PathEscape(templateID) + "/builds/" + url.PathEscape(buildID) + "/status"
	if err := c.doJSON(ctx, http.MethodGet, path, query, nil, &response); err != nil {
		return TemplateBuildStatusResponse{}, &BuildError{Message: err.Error()}
	}
	return response, nil
}

// ListTemplates lists templates for the current team.
func (c *Client) ListTemplates(ctx context.Context, teamID string) ([]TemplateInfo, error) {
	query := url.Values{}
	if teamID != "" {
		query.Set("teamID", teamID)
	}
	var response []TemplateInfo
	if err := c.doJSON(ctx, http.MethodGet, "/templates", query, nil, &response); err != nil {
		return nil, &TemplateError{Message: err.Error()}
	}
	return response, nil
}

// GetTemplate returns template metadata and a page of builds.
func (c *Client) GetTemplate(ctx context.Context, templateID string, limit int, nextToken string) (TemplateWithBuilds, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconvItoa(limit))
	}
	if nextToken != "" {
		query.Set("nextToken", nextToken)
	}
	var response TemplateWithBuilds
	path := "/templates/" + url.PathEscape(templateID)
	_, payload, headers, err := c.doFull(ctx, http.MethodGet, c.config.apiURL(), path, query, nil, c.config.Headers)
	if err != nil {
		return TemplateWithBuilds{}, &TemplateError{Message: err.Error()}
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &response); err != nil {
			return TemplateWithBuilds{}, err
		}
	}
	response.NextToken = nextTokenHeader(headers)
	response.HasNext = response.NextToken != ""
	return response, nil
}

// DeleteTemplate deletes a template. It returns false when the template is not found.
func (c *Client) DeleteTemplate(ctx context.Context, templateID string) (bool, error) {
	path := "/templates/" + url.PathEscape(templateID)
	status, _, err := c.do(ctx, http.MethodDelete, c.config.apiURL(), path, nil, nil, c.config.Headers, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		return false, &TemplateError{Message: err.Error()}
	}
	return status != http.StatusNotFound, nil
}

// TemplateExists checks whether an alias exists.
func (c *Client) TemplateExists(ctx context.Context, alias string) (bool, error) {
	path := "/templates/aliases/" + url.PathEscape(alias)
	status, _, err := c.do(ctx, http.MethodGet, c.config.apiURL(), path, nil, nil, c.config.Headers, http.StatusOK, http.StatusForbidden, http.StatusNotFound)
	if err != nil {
		return false, err
	}
	return status != http.StatusNotFound, nil
}

// AssignTemplateTags assigns tags to a template build.
func (c *Client) AssignTemplateTags(ctx context.Context, targetName string, tags []string) (TemplateTagInfo, error) {
	var response TemplateTagInfo
	req := map[string]any{"target": targetName, "tags": tags}
	if err := c.doJSON(ctx, http.MethodPost, "/templates/tags", nil, req, &response); err != nil {
		return TemplateTagInfo{}, &TemplateError{Message: err.Error()}
	}
	return response, nil
}

// RemoveTemplateTags removes tags from a template.
func (c *Client) RemoveTemplateTags(ctx context.Context, name string, tags []string) error {
	req := map[string]any{"name": name, "tags": tags}
	if err := c.doJSON(ctx, http.MethodDelete, "/templates/tags", nil, req, nil); err != nil {
		return &TemplateError{Message: err.Error()}
	}
	return nil
}

// GetTemplateTags returns all tags for a template.
func (c *Client) GetTemplateTags(ctx context.Context, templateID string) ([]TemplateTag, error) {
	var response []TemplateTag
	path := "/templates/" + url.PathEscape(templateID) + "/tags"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &response); err != nil {
		return nil, &TemplateError{Message: err.Error()}
	}
	return response, nil
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
