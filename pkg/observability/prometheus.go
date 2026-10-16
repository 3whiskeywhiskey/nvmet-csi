// Package observability provides Prometheus metrics for the nvmet-csi driver.
package observability

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// namespace is the Prometheus metric namespace prefix for all nvmet-csi metrics.
	namespace = "nvmet_csi"
)

// DiskHealthSnapshot holds a point-in-time disk performance snapshot.
// Used as return type for the RDS disk monitoring callback to avoid
// importing pkg/rds in the observability package (prevents import cycles).
type DiskHealthSnapshot struct {
	ReadOpsPerSecond  float64
	WriteOpsPerSecond float64
	ReadBytesPerSec   float64
	WriteBytesPerSec  float64
	ReadTimeMs        float64
	WriteTimeMs       float64
	WaitTimeMs        float64
	InFlightOps       float64
	ActiveTimeMs      float64
}

// HardwareHealthSnapshot holds a point-in-time hardware health snapshot from SNMP.
// Used as return type for the RDS hardware monitoring callback to avoid
// importing pkg/rds in the observability package (prevents import cycles).
type HardwareHealthSnapshot struct {
	CPUTemperature    float64
	BoardTemperature  float64
	Fan1Speed         float64
	Fan2Speed         float64
	PSU1Power         float64
	PSU2Power         float64
	PSU1Temperature   float64
	PSU2Temperature   float64
	DiskPoolSizeBytes float64
	DiskPoolUsedBytes float64
}

// RDSAuditEvent is a recent guarded RDS mutation decision.
type RDSAuditEvent struct {
	Timestamp time.Time
	Operation string
	Target    string
	Status    string
	Decision  string
	Reason    string
	DryRun    bool
	Duration  time.Duration
}

// Metrics holds all Prometheus metrics for the nvmet-csi driver.
type Metrics struct {
	registry *prometheus.Registry

	// Volume operation metrics
	volumeOpsTotal    *prometheus.CounterVec
	volumeOpsDuration *prometheus.HistogramVec

	// NVMe connection metrics
	nvmeConnectsTotal   *prometheus.CounterVec
	nvmeConnectDuration prometheus.Histogram
	attachmentCountFunc func() int // Callback for active NVMe connections (GaugeFunc)

	// Mount operation metrics
	mountOpsTotal *prometheus.CounterVec

	// Stale mount metrics
	staleMountsDetectedTotal prometheus.Counter
	staleRecoveriesTotal     *prometheus.CounterVec

	// Orphan cleanup metrics
	orphansCleanedTotal prometheus.Counter
	orphanCleanupTotal  *prometheus.CounterVec

	// Kubernetes events metrics
	eventsPostedTotal *prometheus.CounterVec

	// Attachment operation metrics
	attachmentAttachTotal     *prometheus.CounterVec
	attachmentDetachTotal     *prometheus.CounterVec
	attachmentConflictsTotal  prometheus.Counter
	attachmentReconcileTotal  *prometheus.CounterVec
	attachmentOpDuration      *prometheus.HistogramVec
	attachmentGracePeriodUsed prometheus.Counter
	attachmentStaleCleared    prometheus.Counter
	staleVACleanupTotal       *prometheus.CounterVec

	// Migration operation metrics
	migrationsTotal   *prometheus.CounterVec
	migrationDuration prometheus.Histogram
	activeMigrations  prometheus.Gauge

	// RDS connection metrics
	rdsConnectionState   *prometheus.GaugeVec
	rdsReconnectTotal    *prometheus.CounterVec
	rdsReconnectDuration prometheus.Histogram
	rdsMutationTotal     *prometheus.CounterVec
	rdsMutationDecision  *prometheus.CounterVec
	rdsMutationDuration  *prometheus.HistogramVec
	rdsMutationWait      *prometheus.HistogramVec
	rdsCommandTotal      *prometheus.CounterVec
	rdsCommandDuration   *prometheus.HistogramVec
	rdsAuditMu           sync.Mutex
	rdsAuditEvents       []RDSAuditEvent
	rdsAuditLimit        int

	// RDS monitoring callbacks (SSH + SNMP)
	rdsDiskMetricsFunc     func() (*DiskHealthSnapshot, error)     // Callback for RDS disk performance metrics (SSH)
	rdsHardwareMetricsFunc func() (*HardwareHealthSnapshot, error) // Callback for RDS hardware health metrics (SNMP)
}

// NewMetrics creates a new Metrics instance with all metrics registered.
// Uses a custom registry to avoid panics on driver restart (not DefaultRegistry).
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,

		volumeOpsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "volume_operations_total",
				Help:      "Total number of volume operations by type and status",
			},
			[]string{"operation", "status"},
		),

		volumeOpsDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "volume_operation_duration_seconds",
				Help:      "Duration of volume operations in seconds",
				Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
			},
			[]string{"operation"},
		),

		nvmeConnectsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "nvme_connects_total",
				Help:      "Total number of NVMe connection attempts by status",
			},
			[]string{"status"},
		),

		nvmeConnectDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "nvme_connect_duration_seconds",
			Help:      "Duration of NVMe connection establishment in seconds",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}),

		mountOpsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "mount_operations_total",
				Help:      "Total number of mount/unmount operations by type and status",
			},
			[]string{"operation", "status"},
		),

		staleMountsDetectedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "stale_mounts_detected_total",
			Help:      "Total number of stale mounts detected",
		}),

		staleRecoveriesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "stale_recoveries_total",
				Help:      "Total number of stale mount recovery attempts by status",
			},
			[]string{"status"},
		),

		orphansCleanedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "orphans_cleaned_total",
			Help:      "Total number of orphaned NVMe connections cleaned up",
		}),

		orphanCleanupTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "orphan_cleanup_total",
				Help:      "Total number of node orphan cleanup observations by action, result, and dry-run mode",
			},
			[]string{"action", "result", "dry_run"},
		),

		eventsPostedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "events_posted_total",
				Help:      "Total number of Kubernetes events posted by reason",
			},
			[]string{"reason"},
		),

		attachmentAttachTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "attachment",
				Name:      "attach_total",
				Help:      "Total attachment operations by status",
			},
			[]string{"status"}, // success, failure
		),

		attachmentDetachTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "attachment",
				Name:      "detach_total",
				Help:      "Total detachment operations by status",
			},
			[]string{"status"},
		),

		attachmentConflictsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "attachment",
			Name:      "conflicts_total",
			Help:      "Total attachment conflicts (RWO violations)",
		}),

		attachmentReconcileTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "attachment",
				Name:      "reconcile_total",
				Help:      "Total reconciliation actions by type",
			},
			[]string{"action"}, // clear_stale, sync_annotation
		),

		attachmentOpDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "attachment",
				Name:      "operation_duration_seconds",
				Help:      "Duration of attachment operations",
				Buckets:   []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			},
			[]string{"operation"}, // attach, detach, reconcile
		),

		attachmentGracePeriodUsed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "attachment",
			Name:      "grace_period_used_total",
			Help:      "Total times grace period prevented a conflict",
		}),

		attachmentStaleCleared: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "attachment",
			Name:      "stale_cleared_total",
			Help:      "Total stale attachments cleared by reconciler",
		}),

		staleVACleanupTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "attachment",
				Name:      "stale_volumeattachment_cleanup_total",
				Help:      "Total stale VolumeAttachment cleanup observations by result and dry-run mode",
			},
			[]string{"result", "dry_run"},
		),

		migrationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "migration",
				Name:      "migrations_total",
				Help:      "Total number of KubeVirt live migrations by result",
			},
			[]string{"result"}, // success, failed, timeout
		),

		migrationDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "migration",
			Name:      "duration_seconds",
			Help:      "Duration of KubeVirt live migrations in seconds",
			Buckets:   []float64{15, 30, 60, 90, 120, 180, 300, 600},
		}),

		activeMigrations: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "migration",
			Name:      "active_migrations",
			Help:      "Number of currently in-progress KubeVirt live migrations",
		}),

		rdsConnectionState: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "rds",
				Name:      "connection_state",
				Help:      "RDS SSH connection state (1=connected, 0=disconnected)",
			},
			[]string{"address"},
		),

		rdsReconnectTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "rds",
				Name:      "reconnect_total",
				Help:      "Total RDS reconnection attempts by status",
			},
			[]string{"status"}, // success, failure
		),

		rdsReconnectDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "rds",
			Name:      "reconnect_duration_seconds",
			Help:      "Duration of successful RDS reconnections in seconds",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		}),

		rdsMutationTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "rds",
				Name:      "mutations_total",
				Help:      "Total guarded RDS mutation attempts by operation, status, and dry-run mode",
			},
			[]string{"operation", "status", "dry_run"},
		),

		rdsMutationDecision: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "rds",
				Name:      "mutation_decisions_total",
				Help:      "Total high-level RDS mutation decisions by operation, decision, and reason",
			},
			[]string{"operation", "decision", "reason"},
		),

		rdsMutationDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "rds",
				Name:      "mutation_duration_seconds",
				Help:      "Duration of guarded RDS mutations in seconds",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
			},
			[]string{"operation"},
		),

		rdsMutationWait: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "rds",
				Name:      "mutation_rate_limit_wait_seconds",
				Help:      "Time spent waiting for the RDS mutation rate limiter",
				Buckets:   []float64{0, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
			},
			[]string{"operation"},
		),

		rdsCommandTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "rds",
				Name:      "commands_total",
				Help:      "Total RouterOS commands by family, action, class, and status",
			},
			[]string{"family", "action", "class", "status"},
		),

		rdsCommandDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "rds",
				Name:      "command_duration_seconds",
				Help:      "Duration of RouterOS commands in seconds by family, action, and class",
				Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
			},
			[]string{"family", "action", "class"},
		),

		rdsAuditLimit: 128,
	}

	// Register all metrics with the custom registry
	reg.MustRegister(
		m.volumeOpsTotal,
		m.volumeOpsDuration,
		m.nvmeConnectsTotal,
		m.nvmeConnectDuration,
		m.mountOpsTotal,
		m.staleMountsDetectedTotal,
		m.staleRecoveriesTotal,
		m.orphansCleanedTotal,
		m.orphanCleanupTotal,
		m.eventsPostedTotal,
		m.attachmentAttachTotal,
		m.attachmentDetachTotal,
		m.attachmentConflictsTotal,
		m.attachmentReconcileTotal,
		m.attachmentOpDuration,
		m.attachmentGracePeriodUsed,
		m.attachmentStaleCleared,
		m.staleVACleanupTotal,
		m.migrationsTotal,
		m.migrationDuration,
		m.activeMigrations,
		m.rdsConnectionState,
		m.rdsReconnectTotal,
		m.rdsReconnectDuration,
		m.rdsMutationTotal,
		m.rdsMutationDecision,
		m.rdsMutationDuration,
		m.rdsMutationWait,
		m.rdsCommandTotal,
		m.rdsCommandDuration,
	)

	return m
}

// Handler returns an http.Handler for the /metrics endpoint.
// Use promhttp.HandlerFor with the custom registry for proper isolation.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

// SetAttachmentManager registers a GaugeFunc that derives nvme_connections_active
// from the attachment manager's current state. This must be called after the
// AttachmentManager is created. If not called (e.g., node plugin), the metric
// is not registered and won't appear in scrapes.
func (m *Metrics) SetAttachmentManager(countFunc func() int) {
	m.attachmentCountFunc = countFunc

	nvmeConnectionsActive := prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "nvme_connections_active",
			Help:      "Number of active volumes with NVMe/TCP connections (counts volumes, not per-node attachments during migration)",
		},
		func() float64 {
			if m.attachmentCountFunc == nil {
				return 0
			}
			return float64(m.attachmentCountFunc())
		},
	)

	m.registry.MustRegister(nvmeConnectionsActive)
}

// SetRDSMonitoring registers GaugeFunc metrics for RDS monitoring (disk performance + hardware health).
//
// The diskMetricsFunc callback is invoked during Prometheus scrape to fetch disk performance
// data via SSH (/disk monitor-traffic). The hardwareMetricsFunc callback fetches hardware health
// via SNMP (temperature, fans, PSU, disk capacity). A nil hardwareMetricsFunc
// leaves SNMP metrics unregistered while retaining SSH disk metrics.
//
// This must be called after the RDS client is connected. If not called (e.g., node plugin),
// RDS metrics are not registered.
//
// Metrics registered (all gauges, polled on scrape):
//
//	Disk Performance (9 metrics via SSH):
//	  - rds_disk_read_ops_per_second{slot=<slot>}
//	  - rds_disk_write_ops_per_second{slot=<slot>}
//	  - rds_disk_read_bytes_per_second{slot=<slot>}
//	  - rds_disk_write_bytes_per_second{slot=<slot>}
//	  - rds_disk_read_latency_milliseconds{slot=<slot>}
//	  - rds_disk_write_latency_milliseconds{slot=<slot>}
//	  - rds_disk_wait_latency_milliseconds{slot=<slot>}
//	  - rds_disk_in_flight_operations{slot=<slot>}
//	  - rds_disk_active_time_milliseconds{slot=<slot>}
//	Hardware Health (10 metrics via SNMP):
//	  - rds_hardware_cpu_temperature_celsius
//	  - rds_hardware_board_temperature_celsius
//	  - rds_hardware_fan1_speed_rpm
//	  - rds_hardware_fan2_speed_rpm
//	  - rds_hardware_psu1_power_watts
//	  - rds_hardware_psu2_power_watts
//	  - rds_hardware_psu1_temperature_celsius
//	  - rds_hardware_psu2_temperature_celsius
//	  - rds_hardware_disk_pool_size_bytes
//	  - rds_hardware_disk_pool_used_bytes
func (m *Metrics) SetRDSMonitoring(slot string, snmpHost string, snmpCommunity string, diskMetricsFunc func() (*DiskHealthSnapshot, error), hardwareMetricsFunc func() (*HardwareHealthSnapshot, error)) {
	m.rdsDiskMetricsFunc = diskMetricsFunc
	m.rdsHardwareMetricsFunc = hardwareMetricsFunc

	// Helpers: fetch cached snapshots to avoid multiple SSH/SNMP calls per scrape.
	// Prometheus scrapes all metrics at once, so we cache results for 1 second.
	var (
		cachedDiskSnapshot     *DiskHealthSnapshot
		cachedHardwareSnapshot *HardwareHealthSnapshot
		diskCacheTime          time.Time
		hardwareCacheTime      time.Time
		cacheMu                sync.Mutex
	)

	getDiskSnapshot := func() *DiskHealthSnapshot {
		cacheMu.Lock()
		defer cacheMu.Unlock()

		// Cache for 1 second to avoid 9 SSH calls per scrape
		if cachedDiskSnapshot != nil && time.Since(diskCacheTime) < time.Second {
			return cachedDiskSnapshot
		}

		snapshot, err := diskMetricsFunc()
		if err != nil || snapshot == nil {
			// Return zero snapshot on error (metric reports 0, scrape succeeds)
			return &DiskHealthSnapshot{}
		}

		cachedDiskSnapshot = snapshot
		diskCacheTime = time.Now()
		return cachedDiskSnapshot
	}

	getHardwareSnapshot := func() *HardwareHealthSnapshot {
		cacheMu.Lock()
		defer cacheMu.Unlock()

		// Cache for 1 second to avoid 10 SNMP calls per scrape
		if cachedHardwareSnapshot != nil && time.Since(hardwareCacheTime) < time.Second {
			return cachedHardwareSnapshot
		}

		snapshot, err := hardwareMetricsFunc()
		if err != nil || snapshot == nil {
			// Return zero snapshot on error (metric reports 0, scrape succeeds)
			return &HardwareHealthSnapshot{}
		}

		cachedHardwareSnapshot = snapshot
		hardwareCacheTime = time.Now()
		return cachedHardwareSnapshot
	}

	// Disk metrics use slot label
	diskLabels := prometheus.Labels{"slot": slot}

	// Register disk metrics independently of optional SNMP monitoring.
	m.registry.MustRegister(
		// === Disk Performance Metrics (9 metrics via SSH) ===
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "read_ops_per_second",
			Help:        "Current read IOPS from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().ReadOpsPerSecond }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "write_ops_per_second",
			Help:        "Current write IOPS from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().WriteOpsPerSecond }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "read_bytes_per_second",
			Help:        "Current read throughput in bytes per second from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().ReadBytesPerSec }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "write_bytes_per_second",
			Help:        "Current write throughput in bytes per second from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().WriteBytesPerSec }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "read_latency_milliseconds",
			Help:        "Current read latency in milliseconds from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().ReadTimeMs }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "write_latency_milliseconds",
			Help:        "Current write latency in milliseconds from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().WriteTimeMs }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "wait_latency_milliseconds",
			Help:        "Current wait/queue latency in milliseconds from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().WaitTimeMs }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "in_flight_operations",
			Help:        "Current number of in-flight disk operations (queue depth) from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().InFlightOps }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "disk",
			Name:        "active_time_milliseconds",
			Help:        "Disk active/busy time in milliseconds from /disk monitor-traffic (SSH)",
			ConstLabels: diskLabels,
		}, func() float64 { return getDiskSnapshot().ActiveTimeMs }),
	)

	if hardwareMetricsFunc == nil {
		return
	}

	m.registry.MustRegister(

		// === Hardware Health Metrics (10 metrics via SNMP) ===
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "cpu_temperature_celsius",
			Help: "CPU temperature in Celsius from SNMP (MIKROTIK-MIB)",
		}, func() float64 { return getHardwareSnapshot().CPUTemperature }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "board_temperature_celsius",
			Help: "Board temperature in Celsius from SNMP (MIKROTIK-MIB)",
		}, func() float64 { return getHardwareSnapshot().BoardTemperature }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "fan1_speed_rpm",
			Help: "Fan 1 speed in RPM from SNMP (MIKROTIK-MIB)",
		}, func() float64 { return getHardwareSnapshot().Fan1Speed }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "fan2_speed_rpm",
			Help: "Fan 2 speed in RPM from SNMP (MIKROTIK-MIB)",
		}, func() float64 { return getHardwareSnapshot().Fan2Speed }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "psu1_power_watts",
			Help: "PSU 1 power draw in watts from SNMP (MIKROTIK-MIB)",
		}, func() float64 { return getHardwareSnapshot().PSU1Power }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "psu2_power_watts",
			Help: "PSU 2 power draw in watts from SNMP (MIKROTIK-MIB)",
		}, func() float64 { return getHardwareSnapshot().PSU2Power }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "psu1_temperature_celsius",
			Help: "PSU 1 temperature in Celsius from SNMP (MIKROTIK-MIB)",
		}, func() float64 { return getHardwareSnapshot().PSU1Temperature }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "psu2_temperature_celsius",
			Help: "PSU 2 temperature in Celsius from SNMP (MIKROTIK-MIB)",
		}, func() float64 { return getHardwareSnapshot().PSU2Temperature }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "disk_pool_size_bytes",
			Help: "RAID6 disk pool total size in bytes from SNMP (HOST-RESOURCES-MIB)",
		}, func() float64 { return getHardwareSnapshot().DiskPoolSizeBytes }),

		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "rds", Subsystem: "hardware",
			Name: "disk_pool_used_bytes",
			Help: "RAID6 disk pool used space in bytes from SNMP (HOST-RESOURCES-MIB)",
		}, func() float64 { return getHardwareSnapshot().DiskPoolUsedBytes }),
	)
}

// RecordVolumeOp records a volume operation with timing.
// operation should be one of: create, delete, stage, unstage, publish, unpublish.
func (m *Metrics) RecordVolumeOp(operation string, err error, duration time.Duration) {
	status := "success"
	if err != nil {
		status = "failure"
	}
	m.volumeOpsTotal.WithLabelValues(operation, status).Inc()
	m.volumeOpsDuration.WithLabelValues(operation).Observe(duration.Seconds())
}

// RecordNVMeConnect records an NVMe connection attempt.
// On success (err == nil), also records the duration.
func (m *Metrics) RecordNVMeConnect(err error, duration time.Duration) {
	status := "success"
	if err != nil {
		status = "failure"
	}
	m.nvmeConnectsTotal.WithLabelValues(status).Inc()
	if err == nil {
		m.nvmeConnectDuration.Observe(duration.Seconds())
		// nvme_connections_active gauge is derived from AttachmentManager state via GaugeFunc,
		// not incremented here. This survives controller restarts.
	}
}

// RecordNVMeDisconnect is retained for API compatibility.
// The nvme_connections_active gauge is now derived from AttachmentManager state
// via GaugeFunc, so no manual decrement is needed.
func (m *Metrics) RecordNVMeDisconnect() {
	// nvme_connections_active gauge is derived from AttachmentManager state via GaugeFunc.
	// No manual decrement needed -- the gauge queries current state on each scrape.
}

// RecordMountOp records a mount or unmount operation.
// operation should be one of: mount, unmount.
func (m *Metrics) RecordMountOp(operation string, err error) {
	status := "success"
	if err != nil {
		status = "failure"
	}
	m.mountOpsTotal.WithLabelValues(operation, status).Inc()
}

// RecordStaleMountDetected records that a stale mount was detected.
func (m *Metrics) RecordStaleMountDetected() {
	m.staleMountsDetectedTotal.Inc()
}

// RecordStaleRecovery records a stale mount recovery attempt.
func (m *Metrics) RecordStaleRecovery(err error) {
	status := "success"
	if err != nil {
		status = "failure"
	}
	m.staleRecoveriesTotal.WithLabelValues(status).Inc()
}

// RecordOrphanCleaned records that an orphaned NVMe connection was cleaned up.
func (m *Metrics) RecordOrphanCleaned() {
	m.orphansCleanedTotal.Inc()
}

func dryRunLabel(dryRun bool) string {
	if dryRun {
		return "true"
	}
	return "false"
}

// RecordOrphanDetected records a managed orphan found during node cleanup.
func (m *Metrics) RecordOrphanDetected(dryRun bool) {
	m.orphanCleanupTotal.WithLabelValues("detect", "orphan", dryRunLabel(dryRun)).Inc()
}

// RecordOrphanDisconnectSkippedDryRun records a dry-run cleanup decision.
func (m *Metrics) RecordOrphanDisconnectSkippedDryRun() {
	m.orphanCleanupTotal.WithLabelValues("disconnect", "dry_run", "true").Inc()
}

// RecordOrphanDisconnectLimitReached records that cleanup left orphans due to the per-pass cap.
func (m *Metrics) RecordOrphanDisconnectLimitReached() {
	m.orphanCleanupTotal.WithLabelValues("disconnect", "limit_reached", "false").Inc()
}

// RecordOrphanDisconnectAttempt records an actual orphan disconnect attempt.
func (m *Metrics) RecordOrphanDisconnectAttempt() {
	m.orphanCleanupTotal.WithLabelValues("disconnect", "attempt", "false").Inc()
}

// RecordOrphanDisconnectFailure records a failed orphan disconnect attempt.
func (m *Metrics) RecordOrphanDisconnectFailure() {
	m.orphanCleanupTotal.WithLabelValues("disconnect", "failure", "false").Inc()
}

// RecordOrphanDisconnectSuccess records a successful orphan disconnect attempt.
func (m *Metrics) RecordOrphanDisconnectSuccess() {
	m.orphanCleanupTotal.WithLabelValues("disconnect", "success", "false").Inc()
}

// RecordEventPosted records that a Kubernetes event was posted.
// reason should match the event reason constants (e.g., MountFailure, RecoveryFailed).
func (m *Metrics) RecordEventPosted(reason string) {
	m.eventsPostedTotal.WithLabelValues(reason).Inc()
}

// RecordAttachmentOp records an attachment or detachment operation with duration.
// operation should be "attach" or "detach".
func (m *Metrics) RecordAttachmentOp(operation string, err error, duration time.Duration) {
	status := "success"
	if err != nil {
		status = "failure"
	}

	switch operation {
	case "attach":
		m.attachmentAttachTotal.WithLabelValues(status).Inc()
	case "detach":
		m.attachmentDetachTotal.WithLabelValues(status).Inc()
	}

	m.attachmentOpDuration.WithLabelValues(operation).Observe(duration.Seconds())
}

// RecordAttachmentConflict records an RWO attachment conflict.
func (m *Metrics) RecordAttachmentConflict() {
	m.attachmentConflictsTotal.Inc()
}

// RecordGracePeriodUsed records when grace period prevented a conflict.
func (m *Metrics) RecordGracePeriodUsed() {
	m.attachmentGracePeriodUsed.Inc()
}

// RecordStaleAttachmentCleared records when reconciler cleared a stale attachment.
func (m *Metrics) RecordStaleAttachmentCleared() {
	m.attachmentStaleCleared.Inc()
}

// RecordStaleVolumeAttachmentCandidate records a stale VolumeAttachment candidate.
func (m *Metrics) RecordStaleVolumeAttachmentCandidate(dryRun bool) {
	m.staleVACleanupTotal.WithLabelValues("candidate", dryRunLabel(dryRun)).Inc()
}

// RecordStaleVolumeAttachmentPlanned records a cleanup plan for a stale VolumeAttachment.
func (m *Metrics) RecordStaleVolumeAttachmentPlanned(dryRun bool) {
	m.staleVACleanupTotal.WithLabelValues("planned", dryRunLabel(dryRun)).Inc()
}

// RecordStaleVolumeAttachmentDeleteAttempt records an active stale VolumeAttachment delete attempt.
func (m *Metrics) RecordStaleVolumeAttachmentDeleteAttempt() {
	m.staleVACleanupTotal.WithLabelValues("delete_attempt", "false").Inc()
}

// RecordStaleVolumeAttachmentDeleteSuccess records a successful stale VolumeAttachment delete.
func (m *Metrics) RecordStaleVolumeAttachmentDeleteSuccess() {
	m.staleVACleanupTotal.WithLabelValues("delete_success", "false").Inc()
}

// RecordStaleVolumeAttachmentDeleteFailure records a failed stale VolumeAttachment delete.
func (m *Metrics) RecordStaleVolumeAttachmentDeleteFailure() {
	m.staleVACleanupTotal.WithLabelValues("delete_failure", "false").Inc()
}

// RecordStaleVolumeAttachmentDeleteNotFound records a stale VolumeAttachment that disappeared before deletion.
func (m *Metrics) RecordStaleVolumeAttachmentDeleteNotFound() {
	m.staleVACleanupTotal.WithLabelValues("delete_not_found", "false").Inc()
}

// RecordReconcileAction records a reconciliation action.
// action should be "clear_stale" or "sync_annotation".
func (m *Metrics) RecordReconcileAction(action string) {
	m.attachmentReconcileTotal.WithLabelValues(action).Inc()
}

// RecordMigrationStarted records the start of a KubeVirt live migration.
// Increments the active migrations gauge.
func (m *Metrics) RecordMigrationStarted() {
	m.activeMigrations.Inc()
}

// RecordMigrationResult records the completion of a KubeVirt live migration.
// result must be one of: "success", "failed", "timeout".
// Increments the migrations counter, observes duration, and decrements active gauge.
func (m *Metrics) RecordMigrationResult(result string, duration time.Duration) {
	m.migrationsTotal.WithLabelValues(result).Inc()
	m.migrationDuration.Observe(duration.Seconds())
	m.activeMigrations.Dec()
}

// RecordConnectionState records the RDS SSH connection state.
// connected=true sets gauge to 1.0, connected=false sets gauge to 0.0.
func (m *Metrics) RecordConnectionState(address string, connected bool) {
	value := 0.0
	if connected {
		value = 1.0
	}
	m.rdsConnectionState.WithLabelValues(address).Set(value)
}

// RecordReconnectAttempt records an RDS reconnection attempt.
// status should be "success" or "failure".
// On success, also records the reconnection duration.
func (m *Metrics) RecordReconnectAttempt(status string, duration time.Duration) {
	m.rdsReconnectTotal.WithLabelValues(status).Inc()
	if status == "success" {
		m.rdsReconnectDuration.Observe(duration.Seconds())
	}
}

// RecordRDSMutation records a guarded RouterOS/RDS mutation attempt.
func (m *Metrics) RecordRDSMutation(operation string, err error, dryRun bool, duration time.Duration) {
	if m == nil {
		return
	}

	status := "success"
	if err != nil {
		status = "failure"
	}
	dryRunLabel := "false"
	if dryRun {
		dryRunLabel = "true"
	}

	m.rdsMutationTotal.WithLabelValues(operation, status, dryRunLabel).Inc()
	m.rdsMutationDuration.WithLabelValues(operation).Observe(duration.Seconds())
}

// RecordRDSMutationDecision records a high-level observe-before-mutate decision.
func (m *Metrics) RecordRDSMutationDecision(operation, target, decision, reason string) {
	if m == nil {
		return
	}

	m.rdsMutationDecision.WithLabelValues(operation, decision, reason).Inc()
	m.appendRDSAuditEvent(RDSAuditEvent{
		Timestamp: time.Now(),
		Operation: operation,
		Target:    target,
		Status:    "decision",
		Decision:  decision,
		Reason:    reason,
	})
}

// RecordRDSMutationRateLimitWait records wait time before a guarded RDS mutation.
func (m *Metrics) RecordRDSMutationRateLimitWait(operation string, duration time.Duration) {
	if m == nil {
		return
	}

	m.rdsMutationWait.WithLabelValues(operation).Observe(duration.Seconds())
}

// RecordRDSCommand records one RouterOS command observed by the RDS SSH client.
func (m *Metrics) RecordRDSCommand(family, action, class, status string, duration time.Duration) {
	if m == nil {
		return
	}

	m.rdsCommandTotal.WithLabelValues(family, action, class, status).Inc()
	m.rdsCommandDuration.WithLabelValues(family, action, class).Observe(duration.Seconds())
}

// RecordRDSMutationAudit stores a bounded recent history of guarded mutations.
func (m *Metrics) RecordRDSMutationAudit(operation, target, status string, dryRun bool, duration time.Duration) {
	if m == nil {
		return
	}

	m.appendRDSAuditEvent(RDSAuditEvent{
		Timestamp: time.Now(),
		Operation: operation,
		Target:    target,
		Status:    status,
		DryRun:    dryRun,
		Duration:  duration,
	})
}

func (m *Metrics) appendRDSAuditEvent(event RDSAuditEvent) {
	m.rdsAuditMu.Lock()
	defer m.rdsAuditMu.Unlock()

	if m.rdsAuditLimit <= 0 {
		m.rdsAuditLimit = 128
	}
	if len(m.rdsAuditEvents) >= m.rdsAuditLimit {
		copy(m.rdsAuditEvents, m.rdsAuditEvents[1:])
		m.rdsAuditEvents[len(m.rdsAuditEvents)-1] = event
		return
	}
	m.rdsAuditEvents = append(m.rdsAuditEvents, event)
}

// RecentRDSAudit returns a newest-last copy of recent guarded RDS mutations.
func (m *Metrics) RecentRDSAudit() []RDSAuditEvent {
	if m == nil {
		return nil
	}

	m.rdsAuditMu.Lock()
	defer m.rdsAuditMu.Unlock()

	events := make([]RDSAuditEvent, len(m.rdsAuditEvents))
	copy(events, m.rdsAuditEvents)
	return events
}
