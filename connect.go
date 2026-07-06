package e2b

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxConnectEnvelopeSize = 64 << 20

type connectErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type connectStream struct {
	body     io.ReadCloser
	cancel   context.CancelFunc
	reader   *bufio.Reader
	envelope bool
	decoder  *json.Decoder
}

func (s *Sandbox) connectUnary(ctx context.Context, service, method string, request any, response any, user *string, timeout *time.Duration, extraHeaders map[string]string) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	path := "/" + service + "/" + method
	headers := s.sandboxHeaders(user)
	for k, v := range extraHeaders {
		headers[k] = v
	}
	headers["Content-Type"] = "application/json"
	headers["Connect-Protocol-Version"] = "1"

	ctx, cancel := withTimeout(ctx, s.client.config.resolveTimeout(timeout))
	defer cancel()

	target, err := url.JoinPath(s.envdAPIURL, strings.TrimPrefix(path, "/"))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := s.client.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return formatRequestTimeout()
		}
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return mapConnectHTTPError(res.StatusCode, body)
	}
	if len(body) == 0 || response == nil {
		return nil
	}
	if err := json.Unmarshal(body, response); err != nil {
		return fmt.Errorf("decode connect response %s/%s: %w", service, method, err)
	}
	return nil
}

func (s *Sandbox) connectServerStream(ctx context.Context, service, method string, request any, user *string, timeout time.Duration, requestTimeout time.Duration, extraHeaders map[string]string) (*connectStream, error) {
	payload, err := encodeConnectJSONEnvelope(request)
	if err != nil {
		return nil, err
	}
	headers := s.sandboxHeaders(user)
	for k, v := range extraHeaders {
		headers[k] = v
	}
	headers["Content-Type"] = "application/connect+json"
	headers["Connect-Protocol-Version"] = "1"
	headers["Accept"] = "application/connect+json"
	if timeout > 0 {
		headers["Connect-Timeout-Ms"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}

	streamCtx, cancel := context.WithCancel(ctx)

	target, err := url.JoinPath(s.envdAPIURL, service, method)
	if err != nil {
		cancel()
		return nil, err
	}
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := s.doStreamRequest(ctx, req, cancel, requestTimeout)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		cancel()
		return nil, mapConnectHTTPError(res.StatusCode, body)
	}
	contentType := res.Header.Get("Content-Type")
	stream := &connectStream{
		body:     res.Body,
		cancel:   cancel,
		reader:   bufio.NewReader(res.Body),
		envelope: strings.HasPrefix(contentType, "application/connect+json") || strings.HasPrefix(contentType, "application/connect+proto"),
	}
	if !stream.envelope {
		stream.decoder = json.NewDecoder(stream.reader)
	}
	return stream, nil
}

type streamRequestResult struct {
	res *http.Response
	err error
}

func (s *Sandbox) doStreamRequest(ctx context.Context, req *http.Request, cancel context.CancelFunc, requestTimeout time.Duration) (*http.Response, error) {
	timeout := s.client.config.requestContextTimeout(requestTimeout)
	if timeout == 0 {
		res, err := s.client.http.Do(req)
		if err != nil {
			ctxErr := ctx.Err()
			cancel()
			if ctxErr != nil {
				return nil, formatRequestTimeout()
			}
			return nil, err
		}
		return res, nil
	}

	resultCh := make(chan streamRequestResult)
	go func() {
		res, err := s.client.http.Do(req)
		result := streamRequestResult{res: res, err: err}
		if err == nil && res != nil {
			select {
			case resultCh <- result:
			case <-req.Context().Done():
				_ = res.Body.Close()
			}
			return
		}
		select {
		case resultCh <- result:
		case <-req.Context().Done():
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		if result.err != nil {
			ctxErr := ctx.Err()
			cancel()
			if ctxErr != nil {
				return nil, formatRequestTimeout()
			}
			return nil, result.err
		}
		return result.res, nil
	case <-timer.C:
		cancel()
		return nil, formatRequestTimeout()
	case <-ctx.Done():
		cancel()
		return nil, formatRequestTimeout()
	}
}

func encodeConnectJSONEnvelope(request any) ([]byte, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	envelope := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(envelope[1:5], uint32(len(payload)))
	copy(envelope[5:], payload)
	return envelope, nil
}

func (s *connectStream) Close() error {
	if s == nil || s.body == nil {
		return nil
	}
	err := s.body.Close()
	if s.cancel != nil {
		s.cancel()
	}
	return err
}

func (s *connectStream) Next(out any) error {
	if s.envelope {
		return s.nextEnvelope(out)
	}
	if err := s.decoder.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		return err
	}
	return nil
}

func (s *connectStream) nextEnvelope(out any) error {
	header := make([]byte, 5)
	if _, err := io.ReadFull(s.reader, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return io.EOF
		}
		return err
	}
	flags := header[0]
	length := binary.BigEndian.Uint32(header[1:5])
	if length > maxConnectEnvelopeSize {
		return &SandboxError{Message: fmt.Sprintf("connect envelope size %d exceeds maximum %d", length, maxConnectEnvelopeSize)}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(s.reader, payload); err != nil {
		return err
	}
	if flags&0x02 != 0 {
		var end connectErrorBody
		if len(payload) > 0 && json.Unmarshal(payload, &end) == nil {
			if end.Error != nil && end.Error.Message != "" {
				return mapConnectCode(end.Error.Code, end.Error.Message)
			}
			if end.Message != "" {
				return mapConnectCode(end.Code, end.Message)
			}
		}
		return io.EOF
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

func mapConnectHTTPError(status int, body []byte) error {
	var parsed connectErrorBody
	if json.Unmarshal(body, &parsed) == nil {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return mapConnectCode(parsed.Error.Code, parsed.Error.Message)
		}
		if parsed.Message != "" {
			return mapConnectCode(parsed.Code, parsed.Message)
		}
	}
	if status == http.StatusUnauthorized {
		return &AuthenticationError{Message: string(body)}
	}
	if status == http.StatusNotFound {
		return &NotFoundError{Message: string(body)}
	}
	if status == http.StatusTooManyRequests {
		return &RateLimitError{Message: string(body)}
	}
	return &SandboxError{Message: fmt.Sprintf("%d: %s", status, string(body))}
}

func mapConnectCode(code, message string) error {
	switch code {
	case "invalid_argument":
		return &InvalidArgumentError{Message: message}
	case "unauthenticated":
		return &AuthenticationError{Message: message}
	case "not_found":
		return &NotFoundError{Message: message}
	case "unavailable":
		return formatSandboxTimeout(message)
	case "resource_exhausted":
		return &RateLimitError{Message: message + ": Rate limit exceeded, please try again later."}
	case "canceled":
		return &TimeoutError{Message: message + ": This error is likely due to exceeding request timeout."}
	case "deadline_exceeded":
		return &TimeoutError{Message: message + ": This error is likely due to exceeding the operation timeout."}
	default:
		if message == "" {
			message = code
		}
		return &SandboxError{Message: message}
	}
}
