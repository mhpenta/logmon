# logmon

logmon is a tool that monitors Google Cloud Logging for specific logs and streams them to a web browser. 

## Usage

Populate xml config in directory: .logmon.xml
Build and run:

```bash
go run . 
```

Open browser and navigate to (example port):

```bash
http://localhost:9000
```

## Configuration

Set project, credentials, and log filter in .logmon.xml

Lookback is the start time which logs are pulled from.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config>
    <project_id>project-id</project_id>
    <credentials_path>path/to/local/credentials.json</credentials_path>
    <log_filter>resource.type="gce_instance"</log_filter>
    <server_port>9000</server_port>
    <log_level>INFO</log_level>
    <poll_interval_seconds>5</poll_interval_seconds>
    <max_poll_interval_seconds>300</max_poll_interval_seconds>
    <initial_lookback_seconds>120</initial_lookback_seconds>
    <log_filter>
        <resource_type>cloud_run_revision</resource_type>
        <service_name>service-name</service_name>
        <custom_filter></custom_filter>
    </log_filter>
</config>
```

## Release Notes

Since this version nicely populates the browser with log data at different levels, we are releasing this as-is. 

The main point of this tool is to be use as a foundation for local monitoring tools of Google Cloud Run deployed services which can trigger other actions when errors and warnings are encountered. 

