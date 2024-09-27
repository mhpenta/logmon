package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/logging/logadmin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cloud.google.com/go/logging"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type LogFilter struct {
	ResourceType string `xml:"resource_type"`
	ServiceName  string `xml:"service_name"`
	CustomFilter string `xml:"custom_filter"`
}

type Configuration struct {
	XMLName         xml.Name  `xml:"config"`
	ProjectID       string    `xml:"project_id"`
	Credentials     string    `xml:"credentials_path"`
	LogFilter       LogFilter `xml:"log_filter"`
	ServerPort      string    `xml:"server_port"`
	LogLevel        string    `xml:"log_level"`
	PollInterval    int       `xml:"poll_interval_seconds"`
	MaxPollInterval int       `xml:"max_poll_interval_seconds"`
	InitialLookBack int       `xml:"initial_lookback_seconds"`
	MaxLogsInMemory int       `xml:"max_logs_in_memory"`
}

// LogProcessor interface defines methods for processing different log levels
// Update the LogProcessor interface
type LogProcessor interface {
	ProcessInfo(entry *logging.Entry)
	ProcessWarning(entry *logging.Entry)
	ProcessError(entry *logging.Entry)
	ProcessCritical(entry *logging.Entry)
}

func main() {
	config, err := loadConfig(".logmon.xml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx := context.Background()

	adminClient, err := logadmin.NewClient(ctx, config.ProjectID, option.WithCredentialsFile(config.Credentials))
	if err != nil {
		log.Fatalf("Failed to create log admin client: %v", err)
	}
	defer adminClient.Close()

	processor := NewWebSlogProcessor(config.LogFilter.ServiceName, config.MaxLogsInMemory, true)
	go func() {
		log.Printf("Starting server on :%s", config.ServerPort)
		log.Fatal(http.ListenAndServe(":"+config.ServerPort, processor))
	}()

	monitorLogs(ctx, adminClient, config, config.ProjectID, processor)
}

func monitorLogsv1(ctx context.Context, client *logadmin.Client, filter LogFilter, projectID string, processor LogProcessor) {
	for {
		filterString := buildFilterString(filter)
		iter := client.Entries(ctx,
			logadmin.ProjectIDs([]string{projectID}),
			logadmin.Filter(filterString),
			logadmin.NewestFirst(),
		)

		for {
			entry, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				log.Printf("Error reading log entry: %v", err)
				break
			}

			processLogEntry(entry, processor)
		}

		// Give time for logs to populate, avoid rate limits, no real time requirements
		time.Sleep(5 * time.Second)
	}
}

func monitorLogs(
	ctx context.Context,
	client *logadmin.Client,
	cfg *Configuration,
	projectID string,
	processor LogProcessor) {

	baseInterval := time.Duration(cfg.PollInterval) * time.Second
	maxInterval := time.Duration(cfg.MaxPollInterval) * time.Second

	retries := 0
	lastTimestamp := time.Now().Add(time.Duration(-cfg.InitialLookBack) * time.Second)

	for {
		filterString := buildFilterString(cfg.LogFilter)
		filterString = fmt.Sprintf("%s AND timestamp > \"%s\"", filterString, lastTimestamp.Format(time.RFC3339))

		iter := client.Entries(ctx,
			logadmin.ProjectIDs([]string{projectID}),
			logadmin.Filter(filterString),
			logadmin.NewestFirst(),
		)

		entryCount := 0
		var newestTimestamp time.Time
		for {

			entry, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				if st, ok := status.FromError(err); ok && st.Code() == codes.ResourceExhausted {
					log.Printf("Rate limit exceeded. Backing off for %v", baseInterval)
					time.Sleep(baseInterval)
					baseInterval = time.Duration(math.Min(float64(maxInterval), float64(baseInterval)*2))
					retries++
					break
				} else {
					log.Printf("Error reading log entry: %v", err)
					break
				}
			}

			processLogEntry(entry, processor)
			if entry.Timestamp.After(newestTimestamp) {
				newestTimestamp = entry.Timestamp
			}
			entryCount++
			retries = 0
			baseInterval = 5 * time.Second
		}

		if entryCount > 0 {
			lastTimestamp = newestTimestamp
			log.Printf("Processed %d log entries. Last timestamp: %v", entryCount, lastTimestamp)
		}

		sleepDuration := time.Duration(math.Min(float64(maxInterval), float64(baseInterval)*math.Pow(2, float64(retries))))
		time.Sleep(sleepDuration)
	}
}

func loadConfig(filename string) (*Configuration, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	var config Configuration
	if err := xml.Unmarshal(bytes, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

func processLogEntry(entry *logging.Entry, processor LogProcessor) {
	severity := entry.Severity
	switch severity {
	case logging.Info:
		processor.ProcessInfo(entry)
	case logging.Warning:
		processor.ProcessWarning(entry)
	case logging.Error:
		processor.ProcessError(entry)
	case logging.Critical:
		processor.ProcessCritical(entry)
	default:
		fmt.Printf("Unhandled severity %v: %v\n", severity, entry.Payload)
	}
}

func buildFilterString(filter LogFilter) string {
	var filterParts []string

	if filter.ResourceType != "" {
		filterParts = append(filterParts, fmt.Sprintf("resource.type=\"%s\"", filter.ResourceType))
	}
	if filter.ServiceName != "" {
		filterParts = append(filterParts, fmt.Sprintf("resource.labels.service_name=\"%s\"", filter.ServiceName))
	}
	if filter.CustomFilter != "" {
		filterParts = append(filterParts, filter.CustomFilter)
	}

	return strings.Join(filterParts, " AND ")
}
