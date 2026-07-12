package main

import "encoding/json"

// sendEvent pushes a Phase 2 system-policy event to the server (WS "event"
// message, handled server-side in server/events.go). Shared by every
// watcher (usbwatch.go, downloadwatch.go, desktopwatch.go, configwatch.go,
// installwatch.go) so each one only needs to build its own metadata map,
// not touch the websocket connection directly.
func sendEvent(agentID, eventType string, metadata map[string]interface{}) {
	msg := map[string]interface{}{
		"type":       "event",
		"agent_id":   agentID,
		"event_type": eventType,
		"metadata":   metadata,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		logMsg("ERROR", "sendEvent marshal failed: type=%s error=%v", eventType, err)
		return
	}
	wsSend(data)
	logMsg("INFO", "event sent: type=%s", eventType)
}
