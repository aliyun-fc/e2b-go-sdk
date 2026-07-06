package e2b

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestVolumeRequestReturnsTransportError(t *testing.T) {
	want := errors.New("connect refused")
	client := mustTestClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, want
	}))
	volume := &Volume{client: client, volumeID: "vol", token: "token", apiURL: "https://volume.test"}
	// transport 恒定返回错误，volumeRequest 走错误分支不产生可关闭的 body。
	_, err := volume.volumeRequest(context.Background(), http.MethodGet, "/volumecontent/vol/file", url.Values{"path": []string{"/x"}}, nil, nil, volumeOptions{}) //nolint:bodyclose // error path returns no response body
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	var timeoutErr *TimeoutError
	if errors.As(err, &timeoutErr) {
		t.Fatalf("transport error was reported as timeout: %v", err)
	}
}
