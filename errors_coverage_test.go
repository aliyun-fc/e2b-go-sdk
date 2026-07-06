package e2b

import (
	"errors"
	"strings"
	"testing"
)

// mcovFactoryError is a sentinel error so parseAPIError's defaultFactory
// branches can be observed by the caller.
type mcovFactoryError struct{ msg string }

func (e *mcovFactoryError) Error() string { return e.msg }

func mcovErrorFactory(msg string) error { return &mcovFactoryError{msg: msg} }

func TestMcovParseAPIError(t *testing.T) {
	t.Run("401 with json message", func(t *testing.T) {
		// Arrange
		body := []byte(`{"code":401,"message":"bad token"}`)

		// Act
		err := parseAPIError(401, body, nil)

		// Assert
		var auth *AuthenticationError
		if !errors.As(err, &auth) {
			t.Fatalf("error = %T, want *AuthenticationError", err)
		}
		if !strings.Contains(auth.Message, "bad token") {
			t.Fatalf("message = %q", auth.Message)
		}
	})

	t.Run("401 without message", func(t *testing.T) {
		err := parseAPIError(401, nil, nil)
		var auth *AuthenticationError
		if !errors.As(err, &auth) {
			t.Fatalf("error = %T, want *AuthenticationError", err)
		}
		if strings.Contains(auth.Message, " - ") {
			t.Fatalf("unexpected suffix in %q", auth.Message)
		}
	})

	t.Run("404 with default factory", func(t *testing.T) {
		err := parseAPIError(404, []byte(`{"message":"missing"}`), mcovErrorFactory)
		var factory *mcovFactoryError
		if !errors.As(err, &factory) {
			t.Fatalf("error = %T, want factory error", err)
		}
		if factory.msg != "missing" {
			t.Fatalf("msg = %q", factory.msg)
		}
	})

	t.Run("404 without factory", func(t *testing.T) {
		err := parseAPIError(404, []byte(`{"message":"nope"}`), nil)
		var notFound *NotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("error = %T, want *NotFoundError", err)
		}
		if !strings.Contains(notFound.Message, "nope") {
			t.Fatalf("message = %q", notFound.Message)
		}
	})

	t.Run("429 with message", func(t *testing.T) {
		err := parseAPIError(429, []byte(`{"message":"slow down"}`), nil)
		var rate *RateLimitError
		if !errors.As(err, &rate) {
			t.Fatalf("error = %T, want *RateLimitError", err)
		}
		if !strings.Contains(rate.Message, "slow down") {
			t.Fatalf("message = %q", rate.Message)
		}
	})

	t.Run("429 without message", func(t *testing.T) {
		err := parseAPIError(429, nil, nil)
		var rate *RateLimitError
		if !errors.As(err, &rate) {
			t.Fatalf("error = %T, want *RateLimitError", err)
		}
		if strings.Contains(rate.Message, " - ") {
			t.Fatalf("unexpected suffix in %q", rate.Message)
		}
	})

	t.Run("default with factory", func(t *testing.T) {
		err := parseAPIError(500, []byte(`{"message":"boom"}`), mcovErrorFactory)
		var factory *mcovFactoryError
		if !errors.As(err, &factory) {
			t.Fatalf("error = %T, want factory error", err)
		}
		if !strings.Contains(factory.msg, "500") || !strings.Contains(factory.msg, "boom") {
			t.Fatalf("msg = %q", factory.msg)
		}
	})

	t.Run("default without factory keeps body", func(t *testing.T) {
		body := []byte(`{"message":"kaboom"}`)
		err := parseAPIError(503, body, nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %T, want *APIError", err)
		}
		if apiErr.StatusCode != 503 || apiErr.Message != "kaboom" {
			t.Fatalf("apiErr = %+v", apiErr)
		}
		if string(apiErr.Body) != string(body) {
			t.Fatalf("body = %q", apiErr.Body)
		}
	})

	t.Run("non-json body falls back to raw text", func(t *testing.T) {
		err := parseAPIError(500, []byte("plain text failure"), nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %T, want *APIError", err)
		}
		if apiErr.Message != "plain text failure" {
			t.Fatalf("message = %q", apiErr.Message)
		}
	})
}

func TestMcovAPIErrorErrorFormatting(t *testing.T) {
	tests := []struct {
		name string
		err  *APIError
		want string
	}{
		{name: "nil receiver", err: nil, want: "<nil>"},
		{name: "with message", err: &APIError{StatusCode: 400, Message: "bad"}, want: "400: bad"},
		{name: "falls back to body", err: &APIError{StatusCode: 500, Body: []byte("raw body")}, want: "500: raw body"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMcovSimpleErrorMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"SandboxError", &SandboxError{Message: "sandbox"}, "sandbox"},
		{"TimeoutError", &TimeoutError{Message: "timeout"}, "timeout"},
		{"InvalidArgumentError", &InvalidArgumentError{Message: "invalid"}, "invalid"},
		{"NotEnoughSpaceError", &NotEnoughSpaceError{Message: "space"}, "space"},
		{"NotFoundError", &NotFoundError{Message: "missing"}, "missing"},
		{"FileNotFoundError", &FileNotFoundError{Message: "no-file"}, "no-file"},
		{"SandboxNotFoundError", &SandboxNotFoundError{Message: "no-sbx"}, "no-sbx"},
		{"AuthenticationError", &AuthenticationError{Message: "auth"}, "auth"},
		{"GitAuthError", &GitAuthError{Message: "git-auth"}, "git-auth"},
		{"GitUpstreamError", &GitUpstreamError{Message: "git-upstream"}, "git-upstream"},
		{"TemplateError", &TemplateError{Message: "template"}, "template"},
		{"RateLimitError", &RateLimitError{Message: "rate"}, "rate"},
		{"BuildError", &BuildError{Message: "build"}, "build"},
		{"FileUploadError", &FileUploadError{Message: "upload"}, "upload"},
		{"VolumeError", &VolumeError{Message: "volume"}, "volume"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("%s Error() = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestMcovCommandExitError(t *testing.T) {
	err := &CommandExitError{Result: CommandResult{ExitCode: 3, Stderr: "boom"}}
	got := err.Error()
	if !strings.Contains(got, "code 3") || !strings.Contains(got, "boom") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestMcovErrorsAsAndIsBehavior(t *testing.T) {
	// TimeoutError produced by helpers must be discoverable through errors.As.
	err := formatSandboxTimeout("op failed")
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("formatSandboxTimeout error = %T, want *TimeoutError", err)
	}
	if !strings.Contains(timeoutErr.Message, "sandbox timeout") {
		t.Fatalf("message = %q", timeoutErr.Message)
	}

	reqErr := formatRequestTimeout()
	if !errors.As(reqErr, &timeoutErr) {
		t.Fatalf("formatRequestTimeout error = %T, want *TimeoutError", reqErr)
	}

	// A NotFoundError wrapped via errors.Join should still be found by errors.As.
	wrapped := errors.Join(errors.New("context"), &NotFoundError{Message: "gone"})
	var notFound *NotFoundError
	if !errors.As(wrapped, &notFound) {
		t.Fatalf("wrapped error not found via errors.As: %v", wrapped)
	}
}
