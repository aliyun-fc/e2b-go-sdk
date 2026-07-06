package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// vcovRecorder captures the last request (and its body) sent through the test
// transport so assertions can inspect method/path/query/headers/body.
type vcovRecorder struct {
	req  *http.Request
	body []byte
}

// vcovRespond returns a roundTripFunc that records the request into rec and
// replies with the supplied status and body.
func vcovRespond(rec *vcovRecorder, status int, body string) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		rec.req = r
		if r.Body != nil {
			rec.body, _ = io.ReadAll(r.Body)
		}
		return jsonResponse(status, body, nil), nil
	}
}

// vcovVolume builds a Volume that talks to a fixed content API URL using the
// shared test client wired to transport.
func vcovVolume(t *testing.T, transport http.RoundTripper) *Volume {
	t.Helper()
	client := mustTestClient(t, transport)
	return &Volume{
		client:   client,
		volumeID: "vol1",
		name:     "vol-name",
		token:    "vol-token",
		apiURL:   "https://volume.test",
	}
}

func TestVolumeCreateVolumeSuccess(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	client := mustTestClient(t, vcovRespond(rec, http.StatusCreated, `{"volumeID":"v1","name":"n1","token":"t1"}`))

	// Act
	vol, err := client.CreateVolume(context.Background(), "n1")

	// Assert
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if rec.req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", rec.req.Method)
	}
	if rec.req.URL.Path != "/volumes" {
		t.Errorf("path = %s, want /volumes", rec.req.URL.Path)
	}
	if !strings.Contains(string(rec.body), `"name":"n1"`) {
		t.Errorf("body = %s, want name field", rec.body)
	}
	if vol.VolumeID() != "v1" || vol.Name() != "n1" || vol.Token() != "t1" {
		t.Errorf("volume = %+v, want v1/n1/t1", vol)
	}
	if vol.apiURL != "https://api.test" {
		t.Errorf("apiURL = %s, want https://api.test", vol.apiURL)
	}
}

func TestVolumeCreateVolumeError(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	client := mustTestClient(t, vcovRespond(rec, http.StatusInternalServerError, `{"message":"boom"}`))

	// Act
	vol, err := client.CreateVolume(context.Background(), "n1")

	// Assert
	if vol != nil {
		t.Fatalf("vol = %v, want nil", vol)
	}
	var ve *VolumeError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want *VolumeError", err)
	}
}

func TestVolumeGetVolumeInfoSuccess(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	client := mustTestClient(t, vcovRespond(rec, http.StatusOK, `{"volumeID":"v1","name":"n1","token":"t1"}`))

	// Act
	info, err := client.GetVolumeInfo(context.Background(), "v 1")

	// Assert
	if err != nil {
		t.Fatalf("GetVolumeInfo: %v", err)
	}
	if rec.req.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", rec.req.Method)
	}
	if rec.req.URL.EscapedPath() != "/volumes/v%201" {
		t.Errorf("escaped path = %s, want /volumes/v%%201", rec.req.URL.EscapedPath())
	}
	if info.VolumeID != "v1" || info.Token != "t1" {
		t.Errorf("info = %+v", info)
	}
}

func TestVolumeGetVolumeInfoErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantNotFnd bool
		wantVolErr bool
	}{
		{name: "not found maps to NotFoundError", status: http.StatusNotFound, body: `{"message":"nope"}`, wantNotFnd: true},
		{name: "server error maps to VolumeError", status: http.StatusInternalServerError, body: `{"message":"boom"}`, wantVolErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			rec := &vcovRecorder{}
			client := mustTestClient(t, vcovRespond(rec, tc.status, tc.body))

			// Act
			_, err := client.GetVolumeInfo(context.Background(), "v1")

			// Assert
			if tc.wantNotFnd {
				var nf *NotFoundError
				if !errors.As(err, &nf) {
					t.Fatalf("error = %v, want *NotFoundError", err)
				}
				if nf.Message != "volume v1 not found" {
					t.Errorf("message = %q, want %q", nf.Message, "volume v1 not found")
				}
			}
			if tc.wantVolErr {
				var ve *VolumeError
				if !errors.As(err, &ve) {
					t.Fatalf("error = %v, want *VolumeError", err)
				}
			}
		})
	}
}

func TestVolumeConnectVolume(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		client := mustTestClient(t, vcovRespond(rec, http.StatusOK, `{"volumeID":"v1","name":"n1","token":"t1"}`))

		// Act
		vol, err := client.ConnectVolume(context.Background(), "v1")

		// Assert
		if err != nil {
			t.Fatalf("ConnectVolume: %v", err)
		}
		if vol.VolumeID() != "v1" {
			t.Errorf("volumeID = %s, want v1", vol.VolumeID())
		}
	})

	t.Run("propagates not found", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		client := mustTestClient(t, vcovRespond(rec, http.StatusNotFound, `{"message":"nope"}`))

		// Act
		_, err := client.ConnectVolume(context.Background(), "v1")

		// Assert
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("error = %v, want *NotFoundError", err)
		}
	})
}

func TestVolumeListVolumes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		client := mustTestClient(t, vcovRespond(rec, http.StatusOK, `[{"volumeID":"v1","name":"n1"},{"volumeID":"v2","name":"n2"}]`))

		// Act
		vols, err := client.ListVolumes(context.Background())

		// Assert
		if err != nil {
			t.Fatalf("ListVolumes: %v", err)
		}
		if rec.req.URL.Path != "/volumes" || rec.req.Method != http.MethodGet {
			t.Errorf("request = %s %s", rec.req.Method, rec.req.URL.Path)
		}
		if len(vols) != 2 || vols[0].VolumeID != "v1" || vols[1].Name != "n2" {
			t.Errorf("vols = %+v", vols)
		}
	})

	t.Run("error", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		client := mustTestClient(t, vcovRespond(rec, http.StatusInternalServerError, `{"message":"boom"}`))

		// Act
		vols, err := client.ListVolumes(context.Background())

		// Assert
		if vols != nil {
			t.Fatalf("vols = %v, want nil", vols)
		}
		var ve *VolumeError
		if !errors.As(err, &ve) {
			t.Fatalf("error = %v, want *VolumeError", err)
		}
	})
}

func TestVolumeDestroyVolume(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		want    bool
		wantErr bool
	}{
		{name: "no content deletes", status: http.StatusNoContent, want: true},
		{name: "ok deletes", status: http.StatusOK, want: true},
		{name: "accepted deletes", status: http.StatusAccepted, want: true},
		{name: "not found returns false", status: http.StatusNotFound, want: false},
		{name: "server error", status: http.StatusInternalServerError, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			rec := &vcovRecorder{}
			client := mustTestClient(t, vcovRespond(rec, tc.status, ``))

			// Act
			ok, err := client.DestroyVolume(context.Background(), "v1")

			// Assert
			if tc.wantErr {
				var ve *VolumeError
				if !errors.As(err, &ve) {
					t.Fatalf("error = %v, want *VolumeError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DestroyVolume: %v", err)
			}
			if rec.req.Method != http.MethodDelete {
				t.Errorf("method = %s, want DELETE", rec.req.Method)
			}
			if ok != tc.want {
				t.Errorf("ok = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestVolumeListContent(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `[{"name":"a","type":"file","path":"/a","size":3}]`))

	// Act
	entries, err := v.List(context.Background(), "/dir", WithVolumeDepth(2))

	// Assert
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.req.URL.Path != "/volumecontent/vol1/dir" {
		t.Errorf("path = %s", rec.req.URL.Path)
	}
	q := rec.req.URL.Query()
	if q.Get("path") != "/dir" || q.Get("depth") != "2" {
		t.Errorf("query = %v", q)
	}
	if len(entries) != 1 || entries[0].Name != "a" || entries[0].Size != 3 {
		t.Errorf("entries = %+v", entries)
	}
}

func TestVolumeMakeDir(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `{"name":"d","type":"dir","path":"/d"}`))

	// Act
	stat, err := v.MakeDir(context.Background(), "/d", WithVolumeMode(0o755), WithVolumeForce(true))

	// Assert
	if err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if rec.req.Method != http.MethodPost || rec.req.URL.Path != "/volumecontent/vol1/dir" {
		t.Errorf("request = %s %s", rec.req.Method, rec.req.URL.Path)
	}
	q := rec.req.URL.Query()
	if q.Get("path") != "/d" || q.Get("mode") != "493" || q.Get("force") != "true" {
		t.Errorf("query = %v", q)
	}
	if stat.Name != "d" {
		t.Errorf("stat = %+v", stat)
	}
}

func TestVolumeGetInfo(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `{"name":"a","type":"file","path":"/a"}`))

	// Act
	stat, err := v.GetInfo(context.Background(), "/a")

	// Assert
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if rec.req.URL.Path != "/volumecontent/vol1/path" || rec.req.URL.Query().Get("path") != "/a" {
		t.Errorf("request = %s?%s", rec.req.URL.Path, rec.req.URL.RawQuery)
	}
	if stat.Path != "/a" {
		t.Errorf("stat = %+v", stat)
	}
}

func TestVolumeExists(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		want    bool
		wantErr bool
	}{
		{name: "present", status: http.StatusOK, want: true},
		{name: "absent", status: http.StatusNotFound, want: false},
		{name: "server error surfaces", status: http.StatusInternalServerError, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			rec := &vcovRecorder{}
			v := vcovVolume(t, vcovRespond(rec, tc.status, `{"name":"a","type":"file","path":"/a"}`))

			// Act
			ok, err := v.Exists(context.Background(), "/a")

			// Assert
			if tc.wantErr {
				if err == nil {
					t.Fatalf("err = nil, want error")
				}
				var ve *VolumeError
				if !errors.As(err, &ve) {
					t.Fatalf("error = %v, want *VolumeError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Exists: %v", err)
			}
			if ok != tc.want {
				t.Errorf("ok = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestVolumeUpdateMetadata(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `{"name":"a","type":"file","path":"/a","uid":1}`))

	// Act
	_, err := v.UpdateMetadata(context.Background(), "/a", WithVolumeUID(1), WithVolumeGID(2), WithVolumeMode(0o600))

	// Assert
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if rec.req.Method != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", rec.req.Method)
	}
	if rec.req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %s", rec.req.Header.Get("Content-Type"))
	}
	var payload map[string]int
	if err := json.Unmarshal(rec.body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["uid"] != 1 || payload["gid"] != 2 || payload["mode"] != 0o600 {
		t.Errorf("payload = %v", payload)
	}
}

func TestVolumeReadFile(t *testing.T) {
	t.Run("text success closes body via cancelReadCloser", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `hello world`))

		// Act
		data, err := v.ReadFile(context.Background(), "/a")

		// Assert
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if data != "hello world" {
			t.Errorf("data = %q", data)
		}
		if rec.req.URL.Path != "/volumecontent/vol1/file" {
			t.Errorf("path = %s", rec.req.URL.Path)
		}
	})

	t.Run("error surfaces before decoding", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		v := vcovVolume(t, vcovRespond(rec, http.StatusInternalServerError, `{"message":"boom"}`))

		// Act
		data, err := v.ReadFile(context.Background(), "/a")

		// Assert
		if data != "" {
			t.Fatalf("data = %q, want empty", data)
		}
		var ve *VolumeError
		if !errors.As(err, &ve) {
			t.Fatalf("error = %v, want *VolumeError", err)
		}
	})
}

func TestVolumeReadFileBytesNotFound(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	v := vcovVolume(t, vcovRespond(rec, http.StatusNotFound, `{"message":"missing"}`))

	// Act
	data, err := v.ReadFileBytes(context.Background(), "/a")

	// Assert
	if data != nil {
		t.Fatalf("data = %v, want nil", data)
	}
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %v, want *NotFoundError", err)
	}
	if nf.Message != "missing" {
		t.Errorf("message = %q, want %q", nf.Message, "missing")
	}
}

func TestVolumeReadFileStream(t *testing.T) {
	t.Run("success returns readable body", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `streamed`))

		// Act
		body, err := v.ReadFileStream(context.Background(), "/a")

		// Assert
		if err != nil {
			t.Fatalf("ReadFileStream: %v", err)
		}
		defer body.Close()
		got, _ := io.ReadAll(body)
		if string(got) != "streamed" {
			t.Errorf("body = %q", got)
		}
	})

	t.Run("error closes body and returns typed error", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		v := vcovVolume(t, vcovRespond(rec, http.StatusInternalServerError, `boom`))

		// Act
		body, err := v.ReadFileStream(context.Background(), "/a")

		// Assert
		if body != nil {
			t.Fatalf("body = %v, want nil", body)
		}
		var ve *VolumeError
		if !errors.As(err, &ve) {
			t.Fatalf("error = %v, want *VolumeError", err)
		}
		if !strings.Contains(ve.Message, "500: boom") {
			t.Errorf("message = %q, want to contain %q", ve.Message, "500: boom")
		}
	})
}

func TestVolumeWriteFileVariants(t *testing.T) {
	t.Run("WriteFileText", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `{"name":"a","type":"file","path":"/a","size":5}`))

		// Act
		stat, err := v.WriteFileText(context.Background(), "/a", "hello", WithVolumeMode(0o644))

		// Assert
		if err != nil {
			t.Fatalf("WriteFileText: %v", err)
		}
		if rec.req.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", rec.req.Method)
		}
		if rec.req.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("content-type = %s", rec.req.Header.Get("Content-Type"))
		}
		if string(rec.body) != "hello" {
			t.Errorf("body = %q", rec.body)
		}
		if rec.req.URL.Query().Get("mode") != "420" {
			t.Errorf("mode query = %s", rec.req.URL.Query().Get("mode"))
		}
		if stat.Size != 5 {
			t.Errorf("stat = %+v", stat)
		}
	})

	t.Run("WriteFileBytes", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `{"name":"a","type":"file","path":"/a"}`))

		// Act
		_, err := v.WriteFileBytes(context.Background(), "/a", []byte("bytes-data"))

		// Assert
		if err != nil {
			t.Fatalf("WriteFileBytes: %v", err)
		}
		if string(rec.body) != "bytes-data" {
			t.Errorf("body = %q", rec.body)
		}
	})

	t.Run("WriteFile error", func(t *testing.T) {
		// Arrange
		rec := &vcovRecorder{}
		v := vcovVolume(t, vcovRespond(rec, http.StatusForbidden, `{"message":"denied"}`))

		// Act
		_, err := v.WriteFile(context.Background(), "/a", bytes.NewReader([]byte("x")))

		// Assert
		var ve *VolumeError
		if !errors.As(err, &ve) {
			t.Fatalf("error = %v, want *VolumeError", err)
		}
	})
}

func TestVolumeRemove(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	v := vcovVolume(t, vcovRespond(rec, http.StatusNoContent, ``))

	// Act
	err := v.Remove(context.Background(), "/a")

	// Assert
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if rec.req.Method != http.MethodDelete || rec.req.URL.Path != "/volumecontent/vol1/path" {
		t.Errorf("request = %s %s", rec.req.Method, rec.req.URL.Path)
	}
	if rec.req.URL.Query().Get("path") != "/a" {
		t.Errorf("path query = %s", rec.req.URL.Query().Get("path"))
	}
}

func TestVolumeRequestSetsHeaders(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	v := vcovVolume(t, vcovRespond(rec, http.StatusOK, ``))

	// Act
	res, err := v.volumeRequest(
		context.Background(),
		http.MethodGet,
		"/volumecontent/vol1/file",
		url.Values{"path": []string{"/x"}},
		nil,
		map[string]string{"X-Custom": "yes"},
		volumeOptions{},
	)

	// Assert
	if err != nil {
		t.Fatalf("volumeRequest: %v", err)
	}
	defer res.Body.Close()
	if got := rec.req.Header.Get("Authorization"); got != "Bearer vol-token" {
		t.Errorf("authorization = %q, want %q", got, "Bearer vol-token")
	}
	if rec.req.Header.Get("User-Agent") == "" {
		t.Error("User-Agent header not set")
	}
	if rec.req.Header.Get("X-Custom") != "yes" {
		t.Errorf("X-Custom = %q, want yes", rec.req.Header.Get("X-Custom"))
	}
	if rec.req.URL.Host != "volume.test" {
		t.Errorf("host = %s, want volume.test", rec.req.URL.Host)
	}
}

func TestVolumeRequestOmitsAuthorizationWithoutToken(t *testing.T) {
	// Arrange
	rec := &vcovRecorder{}
	client := mustTestClient(t, vcovRespond(rec, http.StatusOK, ``))
	v := &Volume{client: client, volumeID: "vol1", apiURL: "https://volume.test"}

	// Act
	res, err := v.volumeRequest(context.Background(), http.MethodGet, "/volumecontent/vol1/file", nil, nil, nil, volumeOptions{})

	// Assert
	if err != nil {
		t.Fatalf("volumeRequest: %v", err)
	}
	defer res.Body.Close()
	if got := rec.req.Header.Get("Authorization"); got != "" {
		t.Errorf("authorization = %q, want empty", got)
	}
}

func TestVolumeRequestExplicitTimeout(t *testing.T) {
	// Arrange: an explicit requestTimeout should be honored on the happy path.
	rec := &vcovRecorder{}
	v := vcovVolume(t, vcovRespond(rec, http.StatusOK, `[]`))

	// Act
	_, err := v.List(context.Background(), "/dir", WithVolumeRequestTimeout(time.Minute))

	// Assert
	if err != nil {
		t.Fatalf("List: %v", err)
	}
}

func TestVolumeRequestCanceledContextReportsTimeout(t *testing.T) {
	// Arrange
	v := vcovVolume(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		// Mimic a real transport aborting when the request context is done.
		return nil, r.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Act
	_, err := v.ReadFileBytes(ctx, "/a")

	// Assert
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want *TimeoutError", err)
	}
}

func TestHandleVolumeHTTPError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantNil    bool
		wantNotFnd bool
		wantMsg    string
	}{
		{name: "2xx returns nil", status: http.StatusOK, body: `ignored`, wantNil: true},
		{name: "404 with json message", status: http.StatusNotFound, body: `{"message":"gone"}`, wantNotFnd: true, wantMsg: "gone"},
		{name: "500 with json message", status: http.StatusInternalServerError, body: `{"message":"oops"}`, wantMsg: "500: oops"},
		{name: "500 with plain body", status: http.StatusInternalServerError, body: `plain text`, wantMsg: "500: plain text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			res := jsonResponse(tc.status, tc.body, nil)
			defer func() { _ = res.Body.Close() }()

			// Act
			err := handleVolumeHTTPError(res)

			// Assert
			if tc.wantNil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if tc.wantNotFnd {
				var nf *NotFoundError
				if !errors.As(err, &nf) {
					t.Fatalf("error = %v, want *NotFoundError", err)
				}
				if nf.Message != tc.wantMsg {
					t.Errorf("message = %q, want %q", nf.Message, tc.wantMsg)
				}
				return
			}
			var ve *VolumeError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want *VolumeError", err)
			}
			if ve.Message != tc.wantMsg {
				t.Errorf("message = %q, want %q", ve.Message, tc.wantMsg)
			}
		})
	}
}

func TestVolumeAPIURL(t *testing.T) {
	t.Run("env override wins", func(t *testing.T) {
		// Arrange
		t.Setenv("E2B_VOLUME_API_URL", "https://env.volume/")

		// Act
		got := volumeAPIURL(Config{Debug: true})

		// Assert
		if got != "https://env.volume" {
			t.Errorf("got = %s, want https://env.volume", got)
		}
	})

	t.Run("debug default", func(t *testing.T) {
		// Arrange
		t.Setenv("E2B_VOLUME_API_URL", "")

		// Act
		got := volumeAPIURL(Config{Debug: true})

		// Assert
		if got != "http://localhost:8080" {
			t.Errorf("got = %s, want http://localhost:8080", got)
		}
	})

	t.Run("production falls back to api url", func(t *testing.T) {
		// Arrange
		t.Setenv("E2B_VOLUME_API_URL", "")

		// Act
		got := volumeAPIURL(Config{APIURL: "https://api.example.com/"})

		// Assert
		if got != "https://api.example.com" {
			t.Errorf("got = %s, want https://api.example.com", got)
		}
	})
}

func TestVolumePathQuery(t *testing.T) {
	// Arrange
	uid, gid, mode := 10, 20, 0o640
	force := true
	options := volumeOptions{uid: &uid, gid: &gid, mode: &mode, force: &force}

	// Act
	q := volumePathQuery("/p", options)

	// Assert
	if q.Get("path") != "/p" {
		t.Errorf("path = %s", q.Get("path"))
	}
	if q.Get("uid") != "10" || q.Get("gid") != "20" || q.Get("mode") != "416" || q.Get("force") != "true" {
		t.Errorf("query = %v", q)
	}
}

func TestVolumeOptionsFromIgnoresNil(t *testing.T) {
	// Arrange & Act
	options := volumeOptionsFrom(nil, WithVolumeUID(7), WithVolumeGID(8), WithVolumeMode(9), WithVolumeForce(true), WithVolumeDepth(3), WithVolumeRequestTimeout(5*time.Second))

	// Assert
	if options.uid == nil || *options.uid != 7 {
		t.Errorf("uid = %v, want 7", options.uid)
	}
	if options.gid == nil || *options.gid != 8 {
		t.Errorf("gid = %v, want 8", options.gid)
	}
	if options.mode == nil || *options.mode != 9 {
		t.Errorf("mode = %v, want 9", options.mode)
	}
	if options.force == nil || !*options.force {
		t.Errorf("force = %v, want true", options.force)
	}
	if options.depth == nil || *options.depth != 3 {
		t.Errorf("depth = %v, want 3", options.depth)
	}
	if options.requestTimeout != 5*time.Second {
		t.Errorf("requestTimeout = %v, want 5s", options.requestTimeout)
	}
}
