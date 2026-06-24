package e2b

import (
	"encoding/json"
	"fmt"
)

type apiErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// APIError is returned when the E2B control plane returns a non-success status.
type APIError struct {
	StatusCode int
	Message    string
	Body       []byte
}

func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message != "" {
		return fmt.Sprintf("%d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%d: %s", e.StatusCode, string(e.Body))
}

// SandboxError is the base error for sandbox data-plane failures.
type SandboxError struct {
	Message string
}

func (e *SandboxError) Error() string { return e.Message }

// TimeoutError is returned when a sandbox, process, stream, or request times out.
type TimeoutError struct {
	Message string
}

func (e *TimeoutError) Error() string { return e.Message }

// InvalidArgumentError is returned when an SDK or API argument is invalid.
type InvalidArgumentError struct {
	Message string
}

func (e *InvalidArgumentError) Error() string { return e.Message }

// NotEnoughSpaceError is returned when the sandbox or volume runs out of space.
type NotEnoughSpaceError struct {
	Message string
}

func (e *NotEnoughSpaceError) Error() string { return e.Message }

// NotFoundError is returned when a resource cannot be found.
type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string { return e.Message }

// FileNotFoundError is returned when a sandbox or volume file cannot be found.
type FileNotFoundError struct {
	Message string
}

func (e *FileNotFoundError) Error() string { return e.Message }

// SandboxNotFoundError is returned when a sandbox cannot be found.
type SandboxNotFoundError struct {
	Message string
}

func (e *SandboxNotFoundError) Error() string { return e.Message }

// AuthenticationError is returned when API or envd authentication fails.
type AuthenticationError struct {
	Message string
}

func (e *AuthenticationError) Error() string { return e.Message }

// GitAuthError is returned when a git operation fails due to authentication.
type GitAuthError struct {
	Message string
}

func (e *GitAuthError) Error() string { return e.Message }

// GitUpstreamError is returned when a git operation requires a missing upstream.
type GitUpstreamError struct {
	Message string
}

func (e *GitUpstreamError) Error() string { return e.Message }

// TemplateError is returned when a template or envd version is incompatible.
type TemplateError struct {
	Message string
}

func (e *TemplateError) Error() string { return e.Message }

// RateLimitError is returned when the API or envd rate limit is exceeded.
type RateLimitError struct {
	Message string
}

func (e *RateLimitError) Error() string { return e.Message }

// BuildError is returned when a template build fails.
type BuildError struct {
	Message string
}

func (e *BuildError) Error() string { return e.Message }

// FileUploadError is returned when a template file upload fails.
type FileUploadError struct {
	Message string
}

func (e *FileUploadError) Error() string { return e.Message }

// VolumeError is the base error for volume operations.
type VolumeError struct {
	Message string
}

func (e *VolumeError) Error() string { return e.Message }

// CommandExitError is returned by CommandHandle.Wait when a command exits non-zero.
type CommandExitError struct {
	Result CommandResult
}

func (e *CommandExitError) Error() string {
	return fmt.Sprintf("command exited with code %d and error:\n%s", e.Result.ExitCode, e.Result.Stderr)
}

func parseAPIError(status int, body []byte, defaultFactory func(string) error) error {
	parsed := apiErrorBody{}
	message := ""
	if len(body) > 0 && json.Unmarshal(body, &parsed) == nil {
		message = parsed.Message
	}
	if message == "" {
		message = string(body)
	}

	switch status {
	case 401:
		if message != "" {
			return &AuthenticationError{Message: fmt.Sprintf("%d: Unauthorized, please check your credentials. - %s", status, message)}
		}
		return &AuthenticationError{Message: fmt.Sprintf("%d: Unauthorized, please check your credentials.", status)}
	case 404:
		if defaultFactory != nil {
			return defaultFactory(message)
		}
		return &NotFoundError{Message: fmt.Sprintf("%d: %s", status, message)}
	case 429:
		if message != "" {
			return &RateLimitError{Message: fmt.Sprintf("%d: Rate limit exceeded, please try again later. - %s", status, message)}
		}
		return &RateLimitError{Message: fmt.Sprintf("%d: Rate limit exceeded, please try again later.", status)}
	default:
		if defaultFactory != nil {
			return defaultFactory(fmt.Sprintf("%d: %s", status, message))
		}
		return &APIError{StatusCode: status, Message: message, Body: body}
	}
}

func formatSandboxTimeout(message string) error {
	return &TimeoutError{Message: fmt.Sprintf("%s: This error is likely due to sandbox timeout. You can modify the sandbox timeout by passing timeout when starting the sandbox or calling SetTimeout on the sandbox with the desired timeout.", message)}
}

func formatRequestTimeout() error {
	return &TimeoutError{Message: "request timed out: use WithRequestTimeout to increase this timeout"}
}
