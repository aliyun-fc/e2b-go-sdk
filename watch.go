package e2b

import (
	"context"
	"time"
)

// WatchHandle manages a sandbox directory watcher.
type WatchHandle struct {
	filesystem *Filesystem
	watcherID  string
	user       *string
}

// WatcherID returns the envd watcher ID.
func (w *WatchHandle) WatcherID() string { return w.watcherID }

// Stop removes the watcher.
func (w *WatchHandle) Stop(ctx context.Context, requestTimeout ...time.Duration) error {
	timeout := firstDuration(requestTimeout)
	return mapFilesystemError(w.filesystem.sandbox.connectUnary(
		ctx,
		"filesystem.Filesystem",
		"RemoveWatcher",
		map[string]string{"watcherId": w.watcherID},
		nil,
		w.user,
		timeout,
		nil,
	))
}

// GetNewEvents polls events since the last call.
func (w *WatchHandle) GetNewEvents(ctx context.Context, requestTimeout ...time.Duration) ([]FilesystemEvent, error) {
	timeout := firstDuration(requestTimeout)
	var response struct {
		Events []filesystemEventJSON `json:"events"`
	}
	err := w.filesystem.sandbox.connectUnary(
		ctx,
		"filesystem.Filesystem",
		"GetWatcherEvents",
		map[string]string{"watcherId": w.watcherID},
		&response,
		w.user,
		timeout,
		nil,
	)
	if err != nil {
		return nil, mapFilesystemError(err)
	}
	events := make([]FilesystemEvent, 0, len(response.Events))
	for _, event := range response.Events {
		events = append(events, event.toEvent())
	}
	return events, nil
}

type filesystemEventJSON struct {
	Name  string         `json:"name"`
	Type  any            `json:"type"`
	Entry *entryInfoJSON `json:"entry,omitempty"`
}

func (e filesystemEventJSON) toEvent() FilesystemEvent {
	var entry *EntryInfo
	if e.Entry != nil {
		if mapped, ok := e.Entry.toEntryInfo(); ok {
			entry = &mapped
		}
	}
	return FilesystemEvent{Name: e.Name, Type: mapProtoEventType(e.Type), Entry: entry}
}

func mapProtoEventType(value any) FilesystemEventType {
	switch v := value.(type) {
	case string:
		switch v {
		case "EVENT_TYPE_CREATE", "create":
			return FilesystemEventCreate
		case "EVENT_TYPE_WRITE", "write":
			return FilesystemEventWrite
		case "EVENT_TYPE_REMOVE", "remove":
			return FilesystemEventRemove
		case "EVENT_TYPE_RENAME", "rename":
			return FilesystemEventRename
		case "EVENT_TYPE_CHMOD", "chmod":
			return FilesystemEventChmod
		}
	case float64:
		switch int(v) {
		case 1:
			return FilesystemEventCreate
		case 2:
			return FilesystemEventWrite
		case 3:
			return FilesystemEventRemove
		case 4:
			return FilesystemEventRename
		case 5:
			return FilesystemEventChmod
		}
	}
	return ""
}

func firstDuration(values []time.Duration) time.Duration {
	if len(values) > 0 {
		return values[0]
	}
	return 0
}
