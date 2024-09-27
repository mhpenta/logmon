package responses

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// SendSSEMessageAndCloseLogError sends a Server-Sent Events (SSE) message to the client with the specified message, and then sends a close event.
func SendSSEMessageAndCloseLogError(w http.ResponseWriter, message string) {
	if err := SendSSEEvent(w, "message", message); err != nil {
		slog.Error("Error sending SSE message event", "error", err)
	}
	if err := SendSSEEvent(w, "close", "Stream ended"); err != nil {
		slog.Error("Error sending SSE close event", "error", err)
	}
}

// SendSSEError sends a Server-Sent Events (SSE) error message to the client with the specified status code, event type, and message.
func SendSSEError(w http.ResponseWriter, statusCode int, eventType string, message string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	errorMsg := fmt.Sprintf("event: error\ndata: {\"type\":\"%s\",\"message\":\"%s\"}\n\n", eventType, message)
	_, err := fmt.Fprint(w, errorMsg)
	if err != nil {
		slog.Error("Error sending SSE error", "status code", statusCode, "error", err)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// StreamStringChanToClientSSE streams data from a string channel to the client using Server-Sent Events (SSE).
// It listens to content and error channels, sending data events to the client as they arrive.
// if finalContent is not empty, it sends it at the very end of the stream
// The function returns the full content as a single concatenated string.
func StreamStringChanToClientSSE(
	ctx context.Context,
	w http.ResponseWriter,
	contentChan <-chan string,
	errChan <-chan error,
	streamID string) string {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return ""
	}

	var fullContent strings.Builder

	sendSSEEvent := func(eventType, data string) error {
		eventMsg := fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data)
		_, err := fmt.Fprint(w, eventMsg)
		if err != nil {
			slog.Error("Error sending SSE event", "event type", eventType, "error", err)
			return err
		}
		flusher.Flush()
		return nil
	}

	if streamID != "" {
		if err := sendSSEEvent("stream-id", streamID); err != nil {
			slog.Error("Error sending stream ID", "error", err)
			return ""
		}
	}

streamLoop:
	for {
		select {
		case content, ok := <-contentChan:
			if !ok {
				break streamLoop
			}
			content = strings.ReplaceAll(content, "\n", "<br>")
			fullContent.WriteString(content)
			if err := sendSSEEvent("message", content); err != nil {
				break streamLoop
			}
		case err, ok := <-errChan:
			if !ok {
				break streamLoop
			}
			if err != nil {
				if sendErr := sendSSEEvent("error", err.Error()); sendErr != nil {
					break streamLoop
				}
				break streamLoop
			}
		case <-ctx.Done():
			err := sendSSEEvent("canceled", "Stream canceled by context")
			if err != nil {
				slog.Error("Error sending SSE canceled event", "error", err)
			}
			break streamLoop
		}
	}

	// Send final close event
	err := sendSSEEvent("close", "Stream ended")
	if err != nil {
		slog.Error("Error sending SSE close event", "error", err)
	}
	return fullContent.String()
}

// SendSSEEvent sends a single Server-Sent Events (SSE) message to the client with the specified event type and data.
func SendSSEEvent(w http.ResponseWriter, eventType string, data string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	eventMsg := fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data)
	_, err := fmt.Fprint(w, eventMsg)
	if err != nil {
		slog.Error("Error sending SSE event", "event type", eventType, "error", err)
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// MimicFullSSEStreamForSingleString mimics a full Server-Sent Events (SSE) stream for a single string summary.
func MimicFullSSEStreamForSingleString(w http.ResponseWriter, summary string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events := []struct {
		event string
		data  string
	}{
		{"", strings.ReplaceAll(summary, "\n", "<br>")},
		{"close", "Stream ended"},
	}

	for _, e := range events {
		if e.event != "" {
			if _, err := fmt.Fprintf(w, "event: %s\n", e.event); err != nil {
				return fmt.Errorf("error writing event: %w", err)
			}
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", e.data); err != nil {
			return fmt.Errorf("error writing data: %w", err)
		}
	}

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	return nil
}
