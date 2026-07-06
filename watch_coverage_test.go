package e2b

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// fcovWatchHandle builds a WatchHandle wired to a mocked RoundTripper so the
// Connect-RPC unary calls behind Stop/GetNewEvents can be exercised.
func fcovWatchHandle(t *testing.T, transport http.RoundTripper) *WatchHandle {
	t.Helper()
	f := fcovFilesystem(t, transport, "0.6.4")
	return &WatchHandle{filesystem: f, watcherID: "watcher-1", user: nil}
}

func TestWatchHandleWatcherID(t *testing.T) {
	// Arrange
	handle := &WatchHandle{watcherID: "abc"}

	// Act / Assert
	if handle.WatcherID() != "abc" {
		t.Fatalf("WatcherID = %q", handle.WatcherID())
	}
}

func TestWatchHandleStopSuccess(t *testing.T) {
	// Arrange
	var seen http.Request
	handle := fcovWatchHandle(t, fcovResponder(http.StatusOK, ``, &seen))

	// Act
	err := handle.Stop(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if seen.URL.Path != "/filesystem.Filesystem/RemoveWatcher" {
		t.Fatalf("path = %q", seen.URL.Path)
	}
}

func TestWatchHandleStopMapsError(t *testing.T) {
	// Arrange
	handle := fcovWatchHandle(t, fcovResponder(http.StatusNotFound, `{}`, nil))

	// Act
	err := handle.Stop(context.Background(), 2*time.Second)

	// Assert
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("Stop error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestWatchHandleGetNewEventsDecodesEvents(t *testing.T) {
	// Arrange: mix of event types plus one carrying an entry.
	body := `{"events":[` +
		`{"name":"a","type":"EVENT_TYPE_CREATE"},` +
		`{"name":"b","type":"write","entry":{"name":"b","path":"/b","type":"file","size":9}}` +
		`]}`
	var seen http.Request
	handle := fcovWatchHandle(t, fcovResponder(http.StatusOK, body, &seen))

	// Act
	events, err := handle.GetNewEvents(context.Background())
	if err != nil {
		t.Fatalf("GetNewEvents: %v", err)
	}

	// Assert
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != FilesystemEventCreate {
		t.Fatalf("event[0].Type = %q", events[0].Type)
	}
	if events[1].Type != FilesystemEventWrite || events[1].Entry == nil || events[1].Entry.Size != 9 {
		t.Fatalf("event[1] = %#v", events[1])
	}
	if seen.URL.Path != "/filesystem.Filesystem/GetWatcherEvents" {
		t.Fatalf("path = %q", seen.URL.Path)
	}
}

func TestWatchHandleGetNewEventsMapsError(t *testing.T) {
	// Arrange
	handle := fcovWatchHandle(t, fcovResponder(http.StatusNotFound, `{}`, nil))

	// Act
	_, err := handle.GetNewEvents(context.Background(), 100*time.Millisecond)

	// Assert
	var nf *FileNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("GetNewEvents error = %T %v, want *FileNotFoundError", err, err)
	}
}

func TestWatchFilesystemEventToEventDropsUnknownEntryType(t *testing.T) {
	// Arrange: an entry with an unrecognized type is dropped, leaving Entry nil.
	raw := filesystemEventJSON{
		Name:  "x",
		Type:  "EVENT_TYPE_REMOVE",
		Entry: &entryInfoJSON{Name: "x", Path: "/x", Type: "mystery"},
	}

	// Act
	event := raw.toEvent()

	// Assert
	if event.Type != FilesystemEventRemove {
		t.Fatalf("Type = %q", event.Type)
	}
	if event.Entry != nil {
		t.Fatalf("Entry = %#v, want nil", event.Entry)
	}
}

func TestWatchMapProtoEventType(t *testing.T) {
	// Arrange / Act / Assert
	cases := []struct {
		in   any
		want FilesystemEventType
	}{
		{"EVENT_TYPE_CREATE", FilesystemEventCreate},
		{"create", FilesystemEventCreate},
		{"EVENT_TYPE_WRITE", FilesystemEventWrite},
		{"write", FilesystemEventWrite},
		{"EVENT_TYPE_REMOVE", FilesystemEventRemove},
		{"remove", FilesystemEventRemove},
		{"EVENT_TYPE_RENAME", FilesystemEventRename},
		{"rename", FilesystemEventRename},
		{"EVENT_TYPE_CHMOD", FilesystemEventChmod},
		{"chmod", FilesystemEventChmod},
		{"unknown", ""},
		{float64(1), FilesystemEventCreate},
		{float64(2), FilesystemEventWrite},
		{float64(3), FilesystemEventRemove},
		{float64(4), FilesystemEventRename},
		{float64(5), FilesystemEventChmod},
		{float64(99), ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := mapProtoEventType(c.in); got != c.want {
			t.Fatalf("mapProtoEventType(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWatchFirstDuration(t *testing.T) {
	// Arrange / Act / Assert
	if got := firstDuration(nil); got != 0 {
		t.Fatalf("firstDuration(nil) = %v, want 0", got)
	}
	if got := firstDuration([]time.Duration{3 * time.Second, time.Minute}); got != 3*time.Second {
		t.Fatalf("firstDuration = %v, want 3s", got)
	}
}
