# RDS Monitoring Design

**Phase:** 28.2 (RDS Health & Performance Monitoring Research)
**Status:** Implemented
**Date:** 2026-02-06

## Overview

The nvmet-csi driver exposes comprehensive monitoring metrics from MikroTik RouterOS via Prometheus
using a dual-approach strategy:

- **SSH Polling**: Disk performance metrics (IOPS, throughput, latency, queue depth) from `/disk monitor-traffic`
- **SNMP Polling**: Hardware health metrics (temperatures, fan speeds, PSU status, disk capacity) from MIKROTIK-MIB

SNMP metrics are disabled until an operator configures a host and community
file. SSH disk metrics remain available without SNMP.

Both data sources are polled on-demand during Prometheus scrapes using GaugeFunc collectors.

## Architecture

```
Prometheus --> /metrics endpoint (controller pod)
                    |
        +-----------+------------+
        |                        |
   GaugeFunc (9 disk)     GaugeFunc (10 hardware)
        |                        |
   SSH: /disk                SNMP: gosnmp
   monitor-traffic           (MIKROTIK-MIB,
   storage-pool once          HOST-RESOURCES-MIB)
        |                        |
        +------------------------+
                    |
            MikroTik RDS
          198.51.100.20 (storage VLAN)
```

**Key design decisions:**
- **Dual approach:** SSH for performance metrics (unavailable via SNMP), SNMP for hardware health (lighter weight).
  Combines best-of-both: comprehensive coverage without excessive SSH overhead.
- **GaugeFunc over background polling:** Metrics fetched only during Prometheus scrape (30-60s),
  no background goroutines consuming resources when metrics aren't collected.
- **Separate 1-second caches:** SSH and SNMP results cached independently to avoid multiple calls per scrape
  (9 disk metrics share one SSH call, 10 hardware metrics share one SNMP call).
- **Storage pool aggregate:** Monitors the aggregate storage pool, not individual PVCs. Low cardinality
  (19 total time series). Per-PVC monitoring deferred to future phase if needed.
- **Error tolerance:** SSH/SNMP failures return 0 for all metrics (no scrape failure). Transient network
  issues don't break Prometheus alerting rules.
- **Same network as storage:** Uses 198.51.100.20 (storage VLAN) for both SSH and SNMP. CSI controller
  runs in environment with access to storage network, same IP used for volume operations.

## Metrics Catalog

### Disk Performance Metrics (9 metrics via SSH)

All disk metrics use namespace `rds`, subsystem `disk`, with `slot` label.

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `rds_disk_read_ops_per_second` | Gauge | ops/s | Current read IOPS |
| `rds_disk_write_ops_per_second` | Gauge | ops/s | Current write IOPS |
| `rds_disk_read_bytes_per_second` | Gauge | bytes/s | Read throughput |
| `rds_disk_write_bytes_per_second` | Gauge | bytes/s | Write throughput |
| `rds_disk_read_latency_milliseconds` | Gauge | ms | Read latency |
| `rds_disk_write_latency_milliseconds` | Gauge | ms | Write latency |
| `rds_disk_wait_latency_milliseconds` | Gauge | ms | Queue wait time |
| `rds_disk_in_flight_operations` | Gauge | count | Queue depth |
| `rds_disk_active_time_milliseconds` | Gauge | ms | Disk busy time |

**Labels:**
- `slot`: Disk slot name (e.g., `storage-pool`)

**Example scrape output:**
```
rds_disk_read_ops_per_second{slot="storage-pool"} 0
rds_disk_write_ops_per_second{slot="storage-pool"} 76
rds_disk_write_bytes_per_second{slot="storage-pool"} 1600000
rds_disk_in_flight_operations{slot="storage-pool"} 0
```

### Hardware Health Metrics (10 metrics via SNMP)

All hardware metrics use namespace `rds`, subsystem `hardware`, with no additional labels.

| Metric | Type | Unit | Description | OID |
|--------|------|------|-------------|-----|
| `rds_hardware_cpu_temperature_celsius` | Gauge | °C | CPU temperature | 1.3.6.1.4.1.14988.1.1.3.100.1.3.17 |
| `rds_hardware_board_temperature_celsius` | Gauge | °C | Board temperature | 1.3.6.1.4.1.14988.1.1.3.100.1.3.7101 |
| `rds_hardware_fan1_speed_rpm` | Gauge | RPM | Fan 1 speed | 1.3.6.1.4.1.14988.1.1.3.100.1.3.7001 |
| `rds_hardware_fan2_speed_rpm` | Gauge | RPM | Fan 2 speed | 1.3.6.1.4.1.14988.1.1.3.100.1.3.7002 |
| `rds_hardware_psu1_power_watts` | Gauge | W | PSU 1 power draw | 1.3.6.1.4.1.14988.1.1.3.100.1.3.7501 |
| `rds_hardware_psu2_power_watts` | Gauge | W | PSU 2 power draw | 1.3.6.1.4.1.14988.1.1.3.100.1.3.7502 |
| `rds_hardware_psu1_temperature_celsius` | Gauge | °C | PSU 1 temperature | 1.3.6.1.4.1.14988.1.1.3.100.1.3.7601 |
| `rds_hardware_psu2_temperature_celsius` | Gauge | °C | PSU 2 temperature | 1.3.6.1.4.1.14988.1.1.3.100.1.3.7602 |
| `rds_hardware_disk_pool_size_bytes` | Gauge | bytes | RAID6 pool total size | 1.3.6.1.2.1.25.2.3.1.5.262170 |
| `rds_hardware_disk_pool_used_bytes` | Gauge | bytes | RAID6 pool used space | 1.3.6.1.2.1.25.2.3.1.6.262170 |

**Example scrape output:**
```
rds_hardware_cpu_temperature_celsius 45
rds_hardware_board_temperature_celsius 38
rds_hardware_fan1_speed_rpm 7500
rds_hardware_psu1_power_watts 700
rds_hardware_disk_pool_size_bytes 8e+12
rds_hardware_disk_pool_used_bytes 1.6e+12
```

## Prometheus Alert Examples

```yaml
# === Disk Performance Alerts (SSH) ===

# High write latency (>50ms average over 5 minutes)
- alert: RDSHighWriteLatency
  expr: avg_over_time(rds_disk_write_latency_milliseconds{slot="storage-pool"}[5m]) > 50
  for: 10m
  labels:
    severity: warning

# Queue depth saturation (>16 in-flight ops sustained)
- alert: RDSQueueSaturation
  expr: rds_disk_in_flight_operations{slot="storage-pool"} > 16
  for: 5m
  labels:
    severity: critical

# Zero I/O when volumes are attached (potential RDS hang)
- alert: RDSNoIOActivity
  expr: rds_disk_write_ops_per_second{slot="storage-pool"} == 0
    and nvmet_csi_nvme_connections_active > 0
  for: 15m
  labels:
    severity: warning

# === Hardware Health Alerts (SNMP) ===

# High CPU temperature
- alert: RDSHighCPUTemperature
  expr: rds_hardware_cpu_temperature_celsius > 80
  for: 5m
  labels:
    severity: warning

# Fan failure or low speed
- alert: RDSFanIssue
  expr: rds_hardware_fan1_speed_rpm < 3000 or rds_hardware_fan2_speed_rpm < 3000
  for: 5m
  labels:
    severity: critical

# PSU failure (zero power draw on either PSU)
- alert: RDSPSUFailure
  expr: rds_hardware_psu1_power_watts == 0 or rds_hardware_psu2_power_watts == 0
  for: 1m
  labels:
    severity: critical

# Disk pool near capacity (>90% full)
- alert: RDSDiskPoolNearFull
  expr: (rds_hardware_disk_pool_used_bytes / rds_hardware_disk_pool_size_bytes) > 0.9
  for: 30m
  labels:
    severity: warning
```

## Configuration

The default disk slot is `storage-pool`. Configure the SNMP target and provide
`snmp-community` in the existing RDS Secret to enable hardware metrics:

```yaml
monitoring:
  enabled: true
  rdsMonitoring:
    enabled: true
    diskSlot: storage-pool
    snmp:
      host: "198.51.100.20" # example only; set your own target
```

## Limitations

1. **"once" modifier verified:** The `/disk monitor-traffic disk=<slot> once` command works correctly
   on RouterOS 7.1+ (exits cleanly after one snapshot). No need for output parsing.
2. **Single disk only:** Currently monitors one storage pool. Multiple disk support requires
   repeated SSH commands and increases cardinality.
3. **SNMP provides hardware only:** SNMP does NOT expose disk performance metrics (IOPS, latency).
   MIKROTIK-MIB provides hardware health, HOST-RESOURCES-MIB provides disk capacity. SSH required
   for performance data.
4. **SSH overhead:** Each scrape triggers one SSH command (~50-100ms). At 30s scrape interval,
   this is negligible but should be monitored if polling frequency increases.
5. **SNMP overhead:** Each scrape triggers one SNMP query (~10-20ms). Negligible at standard intervals.
6. **Network access required:** The controller must reach the configured RDS SSH
   endpoint and, when enabled, the configured SNMP host.

## Implementation Details

### SSH Command Format
```bash
# Correct format (no user prefix, use mgmt IP)
ssh 192.0.2.10 '/disk monitor-traffic disk=storage-pool once'

# Incorrect (user prefix causes issues)
ssh admin@198.51.100.20 '/disk monitor-traffic disk=storage-pool once'
```

### SNMP Configuration
```go
// gosnmp v1.37.0 dependency
import "github.com/gosnmp/gosnmp"

// Connection parameters (use storage VLAN IP, same as SSH operations)
snmp := &gosnmp.GoSNMP{
    Target:    "198.51.100.20",
    Port:      161,
    Community: communityFromSecret,
    Version:   gosnmp.Version2c,
    Timeout:   time.Second * 2,
}
```

## Open Questions for Hardware Validation

1. ✅ Does `/disk monitor-traffic disk=<slot> once` work on RouterOS 7.1+? **CONFIRMED: YES**
2. What is the SSH command overhead on RDS CPU? Measure before/after enabling metrics.
3. What are realistic alert thresholds for RAID6 Btrfs on RDS? Baseline during normal operation.
4. What are normal temperature/fan/PSU ranges for this hardware? Baseline during normal operation.

## Related

- Phase 28.1: Fixed `nvmet_csi_nvme_connections_active` metric accuracy (GaugeFunc pattern established)
- Phase 28.2-01: SSH client GetDiskMetrics and SNMP client GetHardwareHealth implementation
- Phase 28: Helm chart will expose monitoring configuration as chart values
- Research: `.planning/phases/28.2-rds-health-performance-monitoring-research/28.2-RESEARCH.md`
