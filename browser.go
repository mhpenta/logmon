package main

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhpenta/logmon/ext/httpext/responses"

	"cloud.google.com/go/logging"
	structpb "github.com/golang/protobuf/ptypes/struct"
)

type WebSlogProcessor struct {
	mu              sync.Mutex
	logs            []string
	clients         map[chan string]bool
	minLevel        logging.Severity
	smoothBroadcast bool
	buffer          []string
	ticker          *time.Ticker
	done            chan bool
	maxLogsInMemory int
	serviceName     string
}

func NewWebSlogProcessor(serviceName string, maxLogs int, smoothBroadcast bool) *WebSlogProcessor {
	p := &WebSlogProcessor{
		clients:         make(map[chan string]bool),
		minLevel:        logging.Warning,
		smoothBroadcast: smoothBroadcast,
		buffer:          make([]string, 0),
		done:            make(chan bool),
		maxLogsInMemory: maxLogs,
		serviceName:     serviceName,
	}
	go p.broadcastLoop()
	return p
}

func (p *WebSlogProcessor) broadcastLoop() {
	p.ticker = time.NewTicker(125 * time.Millisecond)
	defer p.ticker.Stop()

	for {
		select {
		case <-p.ticker.C:
			if p.smoothBroadcast {
				p.smoothBroadcastFunc()
			}
		case <-p.done:
			return
		}
	}
}

func (p *WebSlogProcessor) smoothBroadcastFunc() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.buffer) == 0 {
		return
	}

	message := p.buffer[0]
	p.buffer = p.buffer[1:]

	for client := range p.clients {
		select {
		case client <- message:
		default:
			// If the client's channel is full, skip it
		}
	}
}

func (p *WebSlogProcessor) ProcessInfo(entry *logging.Entry) {
	p.processLog(logging.Info, entry)
}

func (p *WebSlogProcessor) ProcessWarning(entry *logging.Entry) {
	p.processLog(logging.Warning, entry)
}

func (p *WebSlogProcessor) ProcessError(entry *logging.Entry) {
	p.processLog(logging.Error, entry)
}

func (p *WebSlogProcessor) ProcessCritical(entry *logging.Entry) {
	p.processLog(logging.Critical, entry)
}

func (p *WebSlogProcessor) processLogV1(severity logging.Severity, entry *logging.Entry) {
	if severity < p.minLevel {
		return
	}

	logEntry := fmt.Sprintf("%s: %v", severity, entry.Payload)

	p.mu.Lock()
	p.logs = append(p.logs, logEntry)
	p.mu.Unlock()

	p.broadcast(logEntry)
}

func (p *WebSlogProcessor) broadcastV1(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for client := range p.clients {
		slog.Info("Broadcasting message", "msg", message, "size", len(message))
		client <- message
	}
}

func (p *WebSlogProcessor) broadcast(message string) {
	if p.smoothBroadcast {
		p.mu.Lock()
		p.buffer = append(p.buffer, message)
		p.mu.Unlock()
	} else {
		p.mu.Lock()
		defer p.mu.Unlock()
		for client := range p.clients {
			slog.Info("Broadcasting message", "msg", message, "size", len(message))
			client <- message
		}
	}
}

func (p *WebSlogProcessor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		p.serveHTML(w)
	case "/stream":
		p.streamLogs(w, r)
	case "/setLevel":
		p.setLevel(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *WebSlogProcessor) serveHTML(
	w http.ResponseWriter) {

	title := fmt.Sprintf("%s Log Monitor", strings.ToUpper(p.serviceName))

	tmpl := template.Must(template.New("logMonitor").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}</title>
    <style>
        html, body {
            height: 100%;
            margin: 0;
            padding: 0;
            overflow: hidden;
        }
        body {
            font-family: 'Arial', sans-serif;
            background-color: #000;
            color: #fff;
            display: flex;
            flex-direction: column;
        }
        .header {
            display: flex;
            align-items: center;
            justify-content: flex-start;
            padding: 10px 20px;
            background-color: #111;
        }
        h1 {
            color: #0ff;
            text-transform: uppercase;
            letter-spacing: 2px;
            margin: 0;
            margin-right: 20px;
            font-size: 1.5em;
        }
        .controls {
            display: flex;
            flex-wrap: wrap;
        }
        button {
            background-color: #333;
            color: #fff;
            border: none;
            padding: 8px 16px;
            margin: 2px;
            cursor: pointer;
            transition: background-color 0.3s;
            text-transform: uppercase;
            font-weight: bold;
            font-size: 0.8em;
        }
        button:hover {
            background-color: #555;
        }
        button.active {
            background-color: #0ff;
            color: #000;
        }
        #logs {
            flex-grow: 1;
            overflow-y: auto;
            border-top: 1px solid #333;
            padding: 10px;
            background-color: #000;
            font-family: 'Courier New', monospace;
            white-space: pre-wrap;
            word-wrap: break-word;
        }
        #logs div {
            margin-bottom: 2px;
            font-size: 14px;
            line-height: 1.25;
        }
        .highlight-error {
            color: #ff4136;
            font-weight: bold;
        }
        .highlight-warn {
            color: #ffdc00;
            font-weight: bold;
        }
        .highlight-info {
            color: #2ecc40;
            font-weight: bold;
        }
    </style>
    <script>
        let eventSource;
        const MAX_LOG_ENTRIES = {{.MaxLogs}};
        let isPinnedToBottom = true;
        let observer;

        function connectEventSource() {
            eventSource = new EventSource('/stream');
            eventSource.onmessage = function(e) {
                const logs = document.getElementById('logs');
                const newLog = document.createElement('div');
                newLog.innerHTML = highlightKeywords(e.data);
                logs.appendChild(newLog);

                while (logs.children.length > MAX_LOG_ENTRIES) {
                    logs.removeChild(logs.firstChild);
                }

                if (isPinnedToBottom) {
                    scrollToBottom(logs);
                }
            };
        }

        function setLevel(level) {
            fetch('/setLevel?level=' + level)
                .then(response => response.text())
                .then(message => alert(message));
        }

        function highlightKeywords(text) {
            return text
                .replace(/\berror\b/gi, '<span class="highlight-error">$&</span>')
                .replace(/\bwarning\b/gi, '<span class="highlight-warn">$&</span>')
                .replace(/\binfo\b/gi, '<span class="highlight-info">$&</span>');
        }

        function scrollToBottom(element) {
            element.scrollTop = element.scrollHeight;
        }

        function togglePinToBottom() {
            isPinnedToBottom = !isPinnedToBottom;
            const pinButton = document.getElementById('pinButton');
            pinButton.classList.toggle('active');
            pinButton.textContent = isPinnedToBottom ? 'Unpin from Bottom' : 'Pin to Bottom';
            
            if (isPinnedToBottom) {
                scrollToBottom(document.getElementById('logs'));
            }
        }

        function setupMutationObserver() {
            const logs = document.getElementById('logs');
            observer = new MutationObserver(function(mutations) {
                if (isPinnedToBottom) {
                    scrollToBottom(logs);
                }
            });
            observer.observe(logs, { childList: true });
        }

        window.onload = function() {
            connectEventSource();
            document.getElementById('pinButton').addEventListener('click', togglePinToBottom);
            setupMutationObserver();
        };
    </script>
</head>
<body>
    <div class="header">
        <h1>{{.Title}}</h1>
        <div class="controls">
            <button onclick="setLevel('INFO')">INFO</button>
            <button onclick="setLevel('WARNING')">WARNING</button>
            <button onclick="setLevel('ERROR')">ERROR</button>
            <button onclick="setLevel('CRITICAL')">CRITICAL</button>
            <button id="pinButton" class="active">Unpin from Bottom</button>
        </div>
    </div>
    <div id="logs"></div>
</body>
</html>
`))
	data := struct {
		Title   string
		MaxLogs int
	}{
		Title:   title,
		MaxLogs: p.maxLogsInMemory,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Error executing template", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(buf.Bytes())
	if err != nil {
		// At this point, we've already started writing the response,
		// so we can't send an HTTP error. Just log the error.
		fmt.Printf("Error writing response: %v\n", err)
	}
}

func (p *WebSlogProcessor) streamLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	contentChan := make(chan string)
	errChan := make(chan error)

	p.mu.Lock()
	p.clients[contentChan] = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.clients, contentChan)
		p.mu.Unlock()
		close(contentChan)
		close(errChan)
	}()

	go func() {
		<-ctx.Done()
		slog.Info("Client disconnected")
	}()

	responses.StreamStringChanToClientSSE(ctx, w, contentChan, errChan, "")
}

func (p *WebSlogProcessor) setLevel(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	switch level {
	case "INFO":
		p.minLevel = logging.Info
	case "WARNING":
		p.minLevel = logging.Warning
	case "ERROR":
		p.minLevel = logging.Error
	case "CRITICAL":
		p.minLevel = logging.Critical
	default:
		responses.SendSSEError(w, http.StatusBadRequest, "invalid_level", "Invalid log level")
		return
	}
	err := responses.SendSSEEvent(w, "level_set", fmt.Sprintf("Log level set to %s", level))
	if err != nil {
		slog.Error("Error sending SSE message", "error", err)
		return
	}
}

func (p *WebSlogProcessor) processLog(severity logging.Severity, entry *logging.Entry) {
	if severity < p.minLevel {
		return
	}

	logEntry := p.formatLogEntry(severity, entry)

	p.mu.Lock()
	p.logs = append(p.logs, logEntry)
	p.mu.Unlock()

	p.broadcast(logEntry)
}

func (p *WebSlogProcessor) formatLogEntry(severity logging.Severity, entry *logging.Entry) string {
	payloadStruct, ok := entry.Payload.(*structpb.Struct)
	if !ok {
		return fmt.Sprintf("%s: %v", severity, entry.Payload)
	}

	// To filter on specific slog keys, we can get the map from the payload
	/*

		payloadMap := payloadStruct.AsMap()

		for key, value := range payloadMap {
			fmt.Printf("Key: %s - %v \n", key, value)\
		}

	*/

	payloadStr := payloadStruct.String()

	fields := strings.Split(payloadStr, "fields:")

	var parts []string
	parts = append(parts, fmt.Sprintf("%s", severity))

	for _, field := range fields[1:] {
		field = strings.Trim(field, "{}")
		keyValue := strings.SplitN(field, "value:", 2)
		if len(keyValue) != 2 {
			continue
		}

		key := strings.Trim(keyValue[0], " {key:\"}")
		value := strings.Trim(keyValue[1], " {}")

		var actualValue string
		if strings.Contains(value, "string_value:") {
			actualValue = strings.Trim(strings.SplitN(value, "string_value:", 2)[1], "\"")
		} else if strings.Contains(value, "number_value:") {
			numberStr := strings.SplitN(value, "number_value:", 2)[1]
			if number, err := strconv.ParseFloat(numberStr, 64); err == nil {
				if number == float64(int64(number)) {
					actualValue = strconv.FormatInt(int64(number), 10)
				} else {
					actualValue = strconv.FormatFloat(number, 'f', -1, 64)
				}
			} else {
				actualValue = numberStr
			}
		} else if strings.Contains(value, "bool_value:") {
			actualValue = strings.SplitN(value, "bool_value:", 2)[1]
		}

		if key == "timestamp" {
			timestamp, err := time.Parse(time.RFC3339Nano, actualValue)
			if err == nil {
				parts = append([]string{fmt.Sprintf("%s", timestamp.Format("2006-01-02 15:04:05"))}, parts...)
			} else {
				parts = append([]string{fmt.Sprintf("%s", actualValue)}, parts...)
			}
		} else {
			parts = append(parts, fmt.Sprintf("%s: %s", key, actualValue))
		}
	}

	if len(parts) > 2 {
		sort.Slice(parts[2:], func(i, j int) bool {
			return parts[i+2] < parts[j+2]
		})
	}

	return strings.Join(parts, " | ")
}
