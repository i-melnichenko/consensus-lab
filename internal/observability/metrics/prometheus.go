//revive:disable:var-naming
//revive:disable:exported
package metrics

import (
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Prometheus exposes application metrics and can be injected into service/raft layers.
// It implements both internal/service.Metrics and internal/consensus/raft.Metrics
// through method set compatibility, without importing those packages.
type Prometheus struct {
	kvWaitAppliedDuration        *prometheus.HistogramVec
	kvStartToApplyDuration       *prometheus.HistogramVec
	kvApplyToWakeDuration        *prometheus.HistogramVec
	kvWaitAppliedWakeupsTotal    *prometheus.CounterVec
	kvWaitAppliedCallsTotal      *prometheus.CounterVec
	kvProposalTotal              *prometheus.CounterVec
	kvSnapshotDuration           *prometheus.HistogramVec
	kvSnapshotBytes              *prometheus.HistogramVec
	kvSnapshotTotal              *prometheus.CounterVec
	raftAppendEntriesRPCDuration *prometheus.HistogramVec
	raftAppendEntriesRejectTotal *prometheus.CounterVec
	raftAppendEntriesRPCError    *prometheus.CounterVec
	raftInstallSnapshotRPCDur    *prometheus.HistogramVec
	raftInstallSnapshotSendBytes *prometheus.HistogramVec
	raftInstallSnapshotSendTotal *prometheus.CounterVec
	raftElectionStartedTotal     *prometheus.CounterVec
	raftElectionWonTotal         *prometheus.CounterVec
	raftElectionLostTotal        *prometheus.CounterVec
	raftStorageErrorTotal        *prometheus.CounterVec
	raftApplyLag                 *prometheus.GaugeVec
	raftIsLeader                 *prometheus.GaugeVec
	raftStartToCommitDuration    *prometheus.HistogramVec
	raftCommitToApplyDuration    *prometheus.HistogramVec
}

func NewPrometheus(reg prometheus.Registerer) (*Prometheus, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &Prometheus{
		kvWaitAppliedDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "wait_applied_duration_seconds",
				Help:      "Time spent waiting for a proposed command to be applied in the KV service.",
				Buckets:   []float64{0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1},
			},
			[]string{"node_id", "result"},
		),
		kvStartToApplyDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "start_to_apply_duration_seconds",
				Help:      "Time from entering KV waitApplied to the command becoming applied in the state machine.",
				Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5},
			},
			[]string{"node_id"},
		),
		kvApplyToWakeDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "apply_to_waiter_wakeup_duration_seconds",
				Help:      "Time from state machine apply to request waiter completion in KV service.",
				Buckets:   []float64{0.00005, 0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.02},
			},
			[]string{"node_id"},
		),
		kvWaitAppliedWakeupsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "wait_applied_wakeups_total",
				Help:      "Total apply-notify wakeups observed by waitApplied calls.",
			},
			[]string{"node_id"},
		),
		kvWaitAppliedCallsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "wait_applied_calls_total",
				Help:      "Total number of waitApplied calls by result.",
			},
			[]string{"node_id", "result"},
		),
		kvProposalTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "proposal_total",
				Help:      "KV write proposal outcomes (accepted, not_leader, commit_timeout, etc.).",
			},
			[]string{"node_id", "result"},
		),
		kvSnapshotDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "snapshot_duration_seconds",
				Help:      "Duration of KV snapshot creation and handoff to consensus.",
				Buckets:   []float64{0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2},
			},
			[]string{"node_id"},
		),
		kvSnapshotBytes: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "snapshot_bytes",
				Help:      "Serialized KV snapshot payload size in bytes.",
				Buckets:   []float64{256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216},
			},
			[]string{"node_id"},
		),
		kvSnapshotTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "kv",
				Name:      "snapshot_total",
				Help:      "KV snapshot attempts by result.",
			},
			[]string{"node_id", "result"},
		),
		raftAppendEntriesRPCDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "appendentries_rpc_duration_seconds",
				Help:      "Duration of outbound AppendEntries RPC calls from a leader to a peer.",
				Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5},
			},
			[]string{"node_id", "peer_id", "heartbeat"},
		),
		raftAppendEntriesRejectTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "appendentries_reject_total",
				Help:      "Number of AppendEntries rejections received from peers.",
			},
			[]string{"node_id", "peer_id", "heartbeat"},
		),
		raftAppendEntriesRPCError: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "appendentries_rpc_error_total",
				Help:      "Outbound AppendEntries RPC errors by kind.",
			},
			[]string{"node_id", "peer_id", "heartbeat", "kind"},
		),
		raftInstallSnapshotRPCDur: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "installsnapshot_rpc_duration_seconds",
				Help:      "Duration of outbound InstallSnapshot RPC calls.",
				Buckets:   []float64{0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2},
			},
			[]string{"node_id", "peer_id"},
		),
		raftInstallSnapshotSendBytes: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "installsnapshot_send_bytes",
				Help:      "InstallSnapshot payload size sent to a peer in bytes.",
				Buckets:   []float64{256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216},
			},
			[]string{"node_id", "peer_id"},
		),
		raftInstallSnapshotSendTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "installsnapshot_send_total",
				Help:      "InstallSnapshot send attempts by result.",
			},
			[]string{"node_id", "peer_id", "result"},
		),
		raftElectionStartedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "election_started_total",
				Help:      "Number of times a node started an election as candidate.",
			},
			[]string{"node_id"},
		),
		raftElectionWonTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "election_won_total",
				Help:      "Number of elections won by a node.",
			},
			[]string{"node_id"},
		),
		raftElectionLostTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "election_lost_total",
				Help:      "Number of elections lost/aborted by reason.",
			},
			[]string{"node_id", "reason"},
		),
		raftStorageErrorTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "storage_error_total",
				Help:      "Raft storage persistence errors by operation.",
			},
			[]string{"node_id", "op"},
		),
		raftApplyLag: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "apply_lag",
				Help:      "Difference between commitIndex and lastApplied on a node.",
			},
			[]string{"node_id"},
		),
		raftIsLeader: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "is_leader",
				Help:      "1 if node currently believes it is leader, otherwise 0.",
			},
			[]string{"node_id"},
		),
		raftStartToCommitDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "start_to_commit_duration_seconds",
				Help:      "Time from leader accepting a command (StartCommand) to commitIndex covering that entry.",
				Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5},
			},
			[]string{"node_id"},
		),
		raftCommitToApplyDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "consensuslab",
				Subsystem: "raft",
				Name:      "commit_to_apply_duration_seconds",
				Help:      "Time from commitIndex advancing over an entry to that entry being applied.",
				Buckets:   []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1},
			},
			[]string{"node_id"},
		),
	}

	if err := m.register(reg); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Prometheus) register(reg prometheus.Registerer) error {
	if err := registerOrReuseHistogramVec(reg, &m.kvWaitAppliedDuration); err != nil {
		return fmt.Errorf("register kv waitApplied histogram: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.kvStartToApplyDuration); err != nil {
		return fmt.Errorf("register kv start->apply histogram: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.kvApplyToWakeDuration); err != nil {
		return fmt.Errorf("register kv apply->wake histogram: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.kvWaitAppliedWakeupsTotal); err != nil {
		return fmt.Errorf("register kv wait wakeups counter: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.kvWaitAppliedCallsTotal); err != nil {
		return fmt.Errorf("register kv wait calls counter: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.kvProposalTotal); err != nil {
		return fmt.Errorf("register kv proposal counter: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.kvSnapshotDuration); err != nil {
		return fmt.Errorf("register kv snapshot duration histogram: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.kvSnapshotBytes); err != nil {
		return fmt.Errorf("register kv snapshot bytes histogram: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.kvSnapshotTotal); err != nil {
		return fmt.Errorf("register kv snapshot counter: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.raftAppendEntriesRPCDuration); err != nil {
		return fmt.Errorf("register raft appendentries rpc histogram: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.raftAppendEntriesRejectTotal); err != nil {
		return fmt.Errorf("register raft appendentries reject counter: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.raftAppendEntriesRPCError); err != nil {
		return fmt.Errorf("register raft appendentries rpc error counter: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.raftInstallSnapshotRPCDur); err != nil {
		return fmt.Errorf("register raft installsnapshot rpc duration histogram: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.raftInstallSnapshotSendBytes); err != nil {
		return fmt.Errorf("register raft installsnapshot bytes histogram: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.raftInstallSnapshotSendTotal); err != nil {
		return fmt.Errorf("register raft installsnapshot counter: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.raftElectionStartedTotal); err != nil {
		return fmt.Errorf("register raft election started counter: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.raftElectionWonTotal); err != nil {
		return fmt.Errorf("register raft election won counter: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.raftElectionLostTotal); err != nil {
		return fmt.Errorf("register raft election lost counter: %w", err)
	}
	if err := registerOrReuseCounterVec(reg, &m.raftStorageErrorTotal); err != nil {
		return fmt.Errorf("register raft storage error counter: %w", err)
	}
	if err := registerOrReuseGaugeVec(reg, &m.raftApplyLag); err != nil {
		return fmt.Errorf("register raft apply lag gauge: %w", err)
	}
	if err := registerOrReuseGaugeVec(reg, &m.raftIsLeader); err != nil {
		return fmt.Errorf("register raft is_leader gauge: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.raftStartToCommitDuration); err != nil {
		return fmt.Errorf("register raft start->commit histogram: %w", err)
	}
	if err := registerOrReuseHistogramVec(reg, &m.raftCommitToApplyDuration); err != nil {
		return fmt.Errorf("register raft commit->apply histogram: %w", err)
	}
	return nil
}

func registerOrReuseHistogramVec(reg prometheus.Registerer, c **prometheus.HistogramVec) error {
	if err := reg.Register(*c); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			return err
		}
		existing, ok := already.ExistingCollector.(*prometheus.HistogramVec)
		if !ok {
			return fmt.Errorf("collector type mismatch for %T", *c)
		}
		*c = existing
	}
	return nil
}

func registerOrReuseCounterVec(reg prometheus.Registerer, c **prometheus.CounterVec) error {
	if err := reg.Register(*c); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			return err
		}
		existing, ok := already.ExistingCollector.(*prometheus.CounterVec)
		if !ok {
			return fmt.Errorf("collector type mismatch for %T", *c)
		}
		*c = existing
	}
	return nil
}

func registerOrReuseGaugeVec(reg prometheus.Registerer, c **prometheus.GaugeVec) error {
	if err := reg.Register(*c); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			return err
		}
		existing, ok := already.ExistingCollector.(*prometheus.GaugeVec)
		if !ok {
			return fmt.Errorf("collector type mismatch for %T", *c)
		}
		*c = existing
	}
	return nil
}

func (m *Prometheus) ObserveKVWaitAppliedDuration(nodeID string, d time.Duration, ok bool) {
	result := "timeout"
	if ok {
		result = "ok"
	}
	m.kvWaitAppliedDuration.WithLabelValues(nodeID, result).Observe(d.Seconds())
}

func (m *Prometheus) ObserveKVStartToApplyDuration(nodeID string, d time.Duration) {
	m.kvStartToApplyDuration.WithLabelValues(nodeID).Observe(d.Seconds())
}

func (m *Prometheus) ObserveKVApplyToWakeDuration(nodeID string, d time.Duration) {
	m.kvApplyToWakeDuration.WithLabelValues(nodeID).Observe(d.Seconds())
}

func (m *Prometheus) AddKVWaitAppliedWakeups(nodeID string, n int) {
	if n <= 0 {
		return
	}
	m.kvWaitAppliedWakeupsTotal.WithLabelValues(nodeID).Add(float64(n))
}

func (m *Prometheus) IncKVWaitAppliedCall(nodeID string, ok bool) {
	result := "timeout"
	if ok {
		result = "ok"
	}
	m.kvWaitAppliedCallsTotal.WithLabelValues(nodeID, result).Inc()
}

func (m *Prometheus) IncKVProposalResult(nodeID, result string) {
	m.kvProposalTotal.WithLabelValues(nodeID, result).Inc()
}

func (m *Prometheus) ObserveKVSnapshotDuration(nodeID string, d time.Duration) {
	m.kvSnapshotDuration.WithLabelValues(nodeID).Observe(d.Seconds())
}

func (m *Prometheus) ObserveKVSnapshotBytes(nodeID string, n int) {
	if n < 0 {
		n = 0
	}
	m.kvSnapshotBytes.WithLabelValues(nodeID).Observe(float64(n))
}

func (m *Prometheus) IncKVSnapshot(nodeID, result string) {
	m.kvSnapshotTotal.WithLabelValues(nodeID, result).Inc()
}

func (m *Prometheus) ObserveRaftAppendEntriesRPCDuration(nodeID, peerID string, heartbeat bool, d time.Duration) {
	m.raftAppendEntriesRPCDuration.WithLabelValues(nodeID, peerID, boolString(heartbeat)).Observe(d.Seconds())
}

func (m *Prometheus) IncRaftAppendEntriesReject(nodeID, peerID string, heartbeat bool) {
	m.raftAppendEntriesRejectTotal.WithLabelValues(nodeID, peerID, boolString(heartbeat)).Inc()
}

func (m *Prometheus) IncRaftAppendEntriesRPCError(nodeID, peerID string, heartbeat bool, kind string) {
	m.raftAppendEntriesRPCError.WithLabelValues(nodeID, peerID, boolString(heartbeat), kind).Inc()
}

func (m *Prometheus) ObserveRaftInstallSnapshotRPCDuration(nodeID, peerID string, d time.Duration) {
	m.raftInstallSnapshotRPCDur.WithLabelValues(nodeID, peerID).Observe(d.Seconds())
}

func (m *Prometheus) ObserveRaftInstallSnapshotSendBytes(nodeID, peerID string, n int) {
	if n < 0 {
		n = 0
	}
	m.raftInstallSnapshotSendBytes.WithLabelValues(nodeID, peerID).Observe(float64(n))
}

func (m *Prometheus) IncRaftInstallSnapshotSend(nodeID, peerID, result string) {
	m.raftInstallSnapshotSendTotal.WithLabelValues(nodeID, peerID, result).Inc()
}

func (m *Prometheus) IncRaftElectionStarted(nodeID string) {
	m.raftElectionStartedTotal.WithLabelValues(nodeID).Inc()
}

func (m *Prometheus) IncRaftElectionWon(nodeID string) {
	m.raftElectionWonTotal.WithLabelValues(nodeID).Inc()
}

func (m *Prometheus) IncRaftElectionLost(nodeID, reason string) {
	m.raftElectionLostTotal.WithLabelValues(nodeID, reason).Inc()
}

func (m *Prometheus) IncRaftStorageError(nodeID, op string) {
	m.raftStorageErrorTotal.WithLabelValues(nodeID, op).Inc()
}

func (m *Prometheus) SetRaftApplyLag(nodeID string, lag int64) {
	if lag < 0 {
		lag = 0
	}
	m.raftApplyLag.WithLabelValues(nodeID).Set(float64(lag))
}

func (m *Prometheus) SetRaftIsLeader(nodeID string, isLeader bool) {
	if isLeader {
		m.raftIsLeader.WithLabelValues(nodeID).Set(1)
		return
	}
	m.raftIsLeader.WithLabelValues(nodeID).Set(0)
}

func (m *Prometheus) ObserveRaftCommitToApplyDuration(nodeID string, d time.Duration) {
	m.raftCommitToApplyDuration.WithLabelValues(nodeID).Observe(d.Seconds())
}

func (m *Prometheus) ObserveRaftStartToCommitDuration(nodeID string, d time.Duration) {
	m.raftStartToCommitDuration.WithLabelValues(nodeID).Observe(d.Seconds())
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
