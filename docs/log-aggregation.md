# Log Aggregation Guide for nvmet-csi driver

This guide explains how to collect, aggregate, and analyze security logs from the nvmet-csi driver using popular log management platforms.

## Table of Contents

1. [Overview](#overview)
2. [Log Format](#log-format)
3. [Integration with Loki](#integration-with-loki)
4. [Integration with ELK Stack](#integration-with-elk-stack)
5. [Integration with Splunk](#integration-with-splunk)
6. [Integration with Datadog](#integration-with-datadog)
7. [Log Queries and Dashboards](#log-queries-and-dashboards)
8. [Security Monitoring](#security-monitoring)
9. [Alerting](#alerting)
10. [Best Practices](#best-practices)

## Overview

The nvmet-csi driver implements comprehensive security logging through a centralized security logging package (`pkg/security`). All security-relevant events are logged in a structured format with consistent fields for easy parsing and analysis.

### Security Event Categories

- **Authentication** - SSH connections, host key verification
- **Authorization** - Access control decisions
- **Volume Operations** - Create, delete, stage, unstage, publish, unpublish
- **Network Access** - NVMe/TCP connections and disconnections
- **Data Access** - Mount and unmount operations
- **Configuration Changes** - Driver configuration modifications
- **Security Violations** - Validation failures, injection attempts, rate limiting

### Log Severity Levels

- **info** - Normal operations (klog verbosity 2)
- **warning** - Unusual but non-critical events (klog verbosity 1)
- **error** - Errors that may affect functionality (klog verbosity 0)
- **critical** - Security-critical events requiring immediate attention (klog verbosity 0 + JSON)

## Log Format

All security logs follow a structured format:

```
[SECURITY] category=<category> type=<event_type> severity=<severity> outcome=<outcome> msg="<message>" [field=value ...] timestamp=<ISO8601>
```

### Standard Fields

| Field | Description | Example |
|-------|-------------|---------|
| `category` | Event category | `authentication`, `volume_operation` |
| `type` | Specific event type | `ssh_connection_success`, `volume_create_request` |
| `severity` | Severity level | `info`, `warning`, `error`, `critical` |
| `outcome` | Operation outcome | `success`, `failure`, `denied`, `unknown` |
| `msg` | Human-readable message | `"Volume created successfully"` |
| `timestamp` | ISO 8601 timestamp | `2025-01-15T10:30:45.123Z` |

### Identity Fields

| Field | Description | Example |
|-------|-------------|---------|
| `username` | SSH username | `admin` |
| `source_ip` | Source IP address | `10.0.0.1` |
| `target_ip` | Target IP address | `198.51.100.20` |
| `node_id` | Kubernetes node ID | `worker-node-1` |
| `namespace` | Kubernetes namespace | `default` |
| `pod_name` | Pod name | `my-app-pod-abc123` |
| `pvc_name` | PVC name | `data-volume-pvc` |

### Resource Fields

| Field | Description | Example |
|-------|-------------|---------|
| `volume_id` | Volume identifier | `pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890` |
| `volume_name` | Volume name | `data-volume` |
| `nqn` | NVMe Qualified Name | `nqn.2000-02.com.mikrotik:pvc-...` |
| `device_path` | Block device path | `/dev/nvme0n1` |
| `mount_path` | Mount point | `/var/lib/kubelet/pods/.../volumes/...` |

### Operation Fields

| Field | Description | Example |
|-------|-------------|---------|
| `operation` | Operation name | `CreateVolume`, `NodeStageVolume` |
| `duration_ms` | Duration in milliseconds | `1234` |
| `error` | Error message (if any) | `"connection timeout"` |

## Integration with Loki

Grafana Loki is a log aggregation system designed for Kubernetes. It's lightweight and integrates seamlessly with Grafana for visualization.

### Installation

```bash
# Add Grafana Helm repository
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update

# Install Loki stack (Loki + Promtail + Grafana)
helm install loki grafana/loki-stack \
  --namespace monitoring \
  --create-namespace \
  --set grafana.enabled=true \
  --set promtail.enabled=true
```

### Promtail Configuration

Promtail automatically collects logs from all pods. To enhance parsing of nvmet-csi logs, add a pipeline stage:

```yaml
# promtail-config.yaml
scrape_configs:
  - job_name: kubernetes-pods
    pipeline_stages:
      # Parse structured security logs
      - match:
          selector: '{app="nvmet-csi-controller"} |= "[SECURITY]"'
          stages:
            - regex:
                expression: '\[SECURITY\] category=(?P<category>\S+) type=(?P<type>\S+) severity=(?P<severity>\S+) outcome=(?P<outcome>\S+)'
            - labels:
                category:
                type:
                severity:
                outcome:

      # Extract additional fields
      - match:
          selector: '{app="nvmet-csi-controller"} |= "[SECURITY]"'
          stages:
            - regex:
                expression: 'volume_id=(?P<volume_id>\S+)'
            - regex:
                expression: 'node_id=(?P<node_id>\S+)'
            - labels:
                volume_id:
                node_id:
```

### LogQL Queries

#### View All Security Events

```logql
{app="nvmet-csi-controller"} |= "[SECURITY]"
```

#### Filter by Severity

```logql
{app="nvmet-csi-controller"} |= "[SECURITY]" |= "severity=critical"
```

#### Authentication Events

```logql
{app="nvmet-csi-controller"} |= "[SECURITY]" |= "category=authentication"
```

#### Failed Operations

```logql
{app="nvmet-csi-controller"} |= "[SECURITY]" |= "outcome=failure"
```

#### Volume Operations for Specific Volume

```logql
{app="nvmet-csi-controller"} |= "[SECURITY]" |= "category=volume_operation" |= "volume_id=pvc-abc123"
```

#### Security Violations

```logql
{app="nvmet-csi-controller"} |= "[SECURITY]" |= "category=security_violation"
```

### Grafana Dashboard

Create a Grafana dashboard with the following panels:

1. **Security Events Over Time** (Time series)
   ```logql
   sum(count_over_time({app="nvmet-csi-controller"} |= "[SECURITY]"[5m])) by (severity)
   ```

2. **Authentication Failures** (Stat)
   ```logql
   count_over_time({app="nvmet-csi-controller"} |= "[SECURITY]" |= "category=authentication" |= "outcome=failure"[24h])
   ```

3. **Volume Operations by Outcome** (Pie chart)
   ```logql
   sum(count_over_time({app="nvmet-csi-controller"} |= "[SECURITY]" |= "category=volume_operation"[1h])) by (outcome)
   ```

4. **Top Security Violations** (Table)
   ```logql
   topk(10, sum(count_over_time({app="nvmet-csi-controller"} |= "[SECURITY]" |= "category=security_violation"[24h])) by (type))
   ```

## Integration with ELK Stack

The ELK stack (Elasticsearch, Logstash, Kibana) is a popular choice for log aggregation and analysis.

### Installation

```bash
# Add Elastic Helm repository
helm repo add elastic https://helm.elastic.co
helm repo update

# Install Elasticsearch
helm install elasticsearch elastic/elasticsearch \
  --namespace logging \
  --create-namespace

# Install Kibana
helm install kibana elastic/kibana \
  --namespace logging

# Install Filebeat
helm install filebeat elastic/filebeat \
  --namespace logging
```

### Filebeat Configuration

```yaml
# filebeat-config.yaml
filebeat.autodiscover:
  providers:
    - type: kubernetes
      node: ${NODE_NAME}
      hints.enabled: true
      hints.default_config:
        type: container
        paths:
          - /var/log/containers/*-${data.container.id}.log

processors:
  - add_kubernetes_metadata:
      host: ${NODE_NAME}
      matchers:
      - logs_path:
          logs_path: "/var/log/containers/"

  # Parse nvmet-csi security logs
  - dissect:
      tokenizer: "[SECURITY] %{log_content}"
      field: "message"
      target_prefix: "security"
      when:
        contains:
          message: "[SECURITY]"

  # Extract structured fields
  - kv:
      field: "security.log_content"
      field_split: " "
      value_split: "="
      target_field: "security.fields"
      when:
        exists:
          security.log_content: true

output.elasticsearch:
  hosts: ["elasticsearch:9200"]
  index: "nvmet-csi-security-%{+yyyy.MM.dd}"
```

### Elasticsearch Index Template

```json
{
  "index_patterns": ["nvmet-csi-security-*"],
  "mappings": {
    "properties": {
      "security.fields.category": { "type": "keyword" },
      "security.fields.type": { "type": "keyword" },
      "security.fields.severity": { "type": "keyword" },
      "security.fields.outcome": { "type": "keyword" },
      "security.fields.volume_id": { "type": "keyword" },
      "security.fields.node_id": { "type": "keyword" },
      "security.fields.username": { "type": "keyword" },
      "security.fields.source_ip": { "type": "ip" },
      "security.fields.target_ip": { "type": "ip" },
      "security.fields.duration_ms": { "type": "integer" },
      "security.fields.timestamp": { "type": "date" }
    }
  }
}
```

### Kibana Queries

#### All Security Events
```
kubernetes.labels.app:"nvmet-csi-controller" AND message:"[SECURITY]"
```

#### Critical Security Events
```
security.fields.severity:"critical"
```

#### Failed Volume Operations
```
security.fields.category:"volume_operation" AND security.fields.outcome:"failure"
```

#### SSH Authentication Failures
```
security.fields.type:"ssh_connection_failure" OR security.fields.type:"ssh_auth_failure"
```

## Integration with Splunk

Splunk is an enterprise log management platform with powerful search and analytics capabilities.

### Splunk Connect for Kubernetes

```bash
# Install Splunk Connect
helm repo add splunk https://splunk.github.io/splunk-connect-for-kubernetes/
helm repo update

helm install splunk-connect splunk/splunk-connect-for-kubernetes \
  --namespace logging \
  --create-namespace \
  --set splunk.hec.host=<splunk-host> \
  --set splunk.hec.token=<hec-token> \
  --set splunk.hec.indexName=nvmet-csi-security
```

### Splunk Field Extractions

Create field extractions for structured parsing:

```
Settings > Fields > Field extractions > New Field Extraction

Source type: nvmet-csi-logs
Regex:
\[SECURITY\] category=(?<category>\S+) type=(?<type>\S+) severity=(?<severity>\S+) outcome=(?<outcome>\S+).*volume_id=(?<volume_id>\S+).*node_id=(?<node_id>\S+)
```

### SPL Queries

#### Security Events by Severity
```spl
index=nvmet-csi-security "[SECURITY]"
| stats count by severity
| sort -count
```

#### Volume Operation Timeline
```spl
index=nvmet-csi-security category=volume_operation
| timechart count by type
```

#### Failed Operations with Details
```spl
index=nvmet-csi-security outcome=failure
| table _time, category, type, volume_id, node_id, error
| sort -_time
```

#### Authentication Anomalies
```spl
index=nvmet-csi-security category=authentication outcome=failure
| stats count by source_ip, username
| where count > 5
```

## Integration with Datadog

Datadog provides cloud-native monitoring and log management.

### Installation

```bash
# Install Datadog Agent
helm repo add datadog https://helm.datadoghq.com
helm repo update

helm install datadog datadog/datadog \
  --namespace monitoring \
  --create-namespace \
  --set datadog.apiKey=<api-key> \
  --set datadog.logs.enabled=true \
  --set datadog.logs.containerCollectAll=true
```

### Log Processing Pipeline

In Datadog, create a processing pipeline for nvmet-csi logs:

1. **Grok Parser** to extract structured fields:
   ```
   \[SECURITY\] category=%{notSpace:security.category} type=%{notSpace:security.type} severity=%{notSpace:security.severity} outcome=%{notSpace:security.outcome}
   ```

2. **Attribute Remapper** to map extracted fields

3. **Category Processor** to tag logs by security category

### Datadog Queries

```
service:nvmet-csi-controller @security.severity:critical
service:nvmet-csi-controller @security.category:authentication @security.outcome:failure
service:nvmet-csi-controller @security.type:volume_create_failure
```

## Log Queries and Dashboards

### Common Query Patterns

#### Find All Events for a Specific Volume
```
volume_id="pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890"
```

#### Slow Operations (> 1 second)
```
duration_ms > 1000
```

#### Operations by User
```
category="authentication" | stats count by username
```

#### Error Rate Over Time
```
outcome="failure" | timechart span=5m count
```

### Dashboard Panels

1. **Security Events Heatmap** - Shows event distribution by category and severity
2. **Authentication Success/Failure Rate** - Gauge showing auth success percentage
3. **Volume Operation Latency** - Histogram of operation durations
4. **Recent Security Violations** - Table of last 10 security violations
5. **Node Activity Map** - Geographic or logical map of node activity

## Security Monitoring

### Key Metrics to Monitor

1. **Authentication Failure Rate**
   - Alert if > 5 failures in 5 minutes from same IP
   - Indicates potential brute force attack

2. **Host Key Mismatches**
   - Alert on ANY occurrence
   - Critical security indicator of MITM attack

3. **Volume Operation Failure Rate**
   - Alert if > 10% failures
   - May indicate system issues or attacks

4. **Security Violations**
   - Alert on ANY occurrence
   - Validation failures, injection attempts, etc.

5. **Unusual Access Patterns**
   - Large spike in volume operations
   - Operations outside normal hours
   - Operations from unexpected nodes

### SIEM Integration

Export logs to Security Information and Event Management (SIEM) systems:

- **Splunk Enterprise Security**
- **IBM QRadar**
- **Azure Sentinel**
- **Google Chronicle**

## Alerting

### Critical Alerts

#### SSH Host Key Mismatch
```
Alert when: count of (type=ssh_host_key_mismatch) > 0 in 1 minute
Severity: Critical
Action: Page on-call engineer, disable RDS access
```

#### Multiple Authentication Failures
```
Alert when: count of (category=authentication AND outcome=failure) > 5 in 5 minutes from same source_ip
Severity: High
Action: Notify security team, consider IP blocking
```

#### Security Violations
```
Alert when: count of (category=security_violation) > 0 in 1 minute
Severity: Critical
Action: Notify security team, log for investigation
```

#### High Error Rate
```
Alert when: (count of outcome=failure) / (count of all events) > 0.1 in 10 minutes
Severity: Warning
Action: Notify operations team
```

### Sample Alert Rules (Prometheus/Alertmanager)

```yaml
groups:
  - name: nvmet-csi-security
    rules:
      - alert: RDSCSIHostKeyMismatch
        expr: |
          increase(nvmet_csi_security_ssh_host_key_mismatches_total[5m]) > 0
        for: 0m
        labels:
          severity: critical
        annotations:
          summary: "SSH host key mismatch detected"
          description: "Possible MITM attack on RDS connection"

      - alert: RDSCSIAuthFailures
        expr: |
          rate(nvmet_csi_security_ssh_auth_failures_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High SSH authentication failure rate"

      - alert: RDSCSISecurityViolation
        expr: |
          increase(nvmet_csi_security_validation_failures_total[1m]) > 0
        for: 0m
        labels:
          severity: critical
        annotations:
          summary: "Security validation failure detected"
```

## Best Practices

### Log Retention

- **Hot storage** (searchable): 30 days
- **Warm storage** (archived, searchable): 90 days
- **Cold storage** (compliance): 1-7 years depending on requirements

### Performance Considerations

1. **Index Optimization**
   - Create indices on frequently queried fields (severity, category, type, volume_id)
   - Use time-based indices for better performance

2. **Log Volume Management**
   - Adjust klog verbosity levels based on environment (lower in production)
   - Use log sampling for very high-volume systems

3. **Query Optimization**
   - Filter by time range first
   - Use indexed fields in filters
   - Avoid wildcard searches when possible

### Security Best Practices

1. **Access Control**
   - Restrict log access to authorized personnel only
   - Use RBAC for log management platforms
   - Audit log access regularly

2. **Log Integrity**
   - Enable log signing/encryption where supported
   - Use write-once storage for compliance
   - Implement tamper detection

3. **Sensitive Data**
   - Logs are already sanitized by error sanitization module
   - Do not log secrets, tokens, or credentials
   - Redact PII if present

4. **Audit Trail**
   - Maintain logs of who accessed security logs
   - Log all administrative actions
   - Preserve chain of custody for forensics

### Compliance

For regulated environments:

- **GDPR**: Ensure logs don't contain personal data or implement retention policies
- **HIPAA**: Encrypt logs at rest and in transit, implement access controls
- **PCI-DSS**: Retain logs for at least 1 year, protect from unauthorized access
- **SOC 2**: Implement comprehensive logging, monitoring, and alerting

## Troubleshooting

### Missing Logs

1. Check pod logs are being collected:
   ```bash
   kubectl logs -n kube-system -l app=nvmet-csi-controller
   ```

2. Verify log collector is running:
   ```bash
   kubectl get pods -n logging
   ```

3. Check log collector configuration

### Parsing Issues

1. Verify log format matches expected pattern
2. Test regex patterns with sample logs
3. Check for special characters or encoding issues

### Performance Issues

1. Reduce log verbosity
2. Optimize queries (add time ranges, use indexed fields)
3. Scale log infrastructure horizontally
4. Implement log sampling for high-volume systems

## Additional Resources

- [Kubernetes Logging Architecture](https://kubernetes.io/docs/concepts/cluster-administration/logging/)
- [klog Documentation](https://github.com/kubernetes/klog)
- [Loki Documentation](https://grafana.com/docs/loki/latest/)
- [ELK Stack Guide](https://www.elastic.co/guide/index.html)
- [Splunk Kubernetes Guide](https://docs.splunk.com/Documentation/Splunk/latest/Data/UsetheKubernetesdata)
- [Datadog Kubernetes Integration](https://docs.datadoghq.com/integrations/kubernetes/)
