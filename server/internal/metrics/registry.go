package metrics

import (
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/realtime"
)

type RegistryOptions struct {
	Pool        *pgxpool.Pool
	ReplicaPool *pgxpool.Pool
	Realtime    *realtime.Metrics
	DaemonWS    *daemonws.Metrics
	Version     string
	Commit      string
}

type Registry struct {
<<<<<<< HEAD
	Gatherer                    prometheus.Gatherer
	HTTP                        *HTTPMetrics
	Business                    *BusinessMetrics
	ChannelMedia                *ChannelMediaReconcilerMetrics
	ChannelDelivery             *ChannelDeliveryMetrics
	Wecom                       *WecomMetrics
	RoleSource                  *RoleSourceMetrics
	RoleSourceArtifactGC        *RoleSourceArtifactGCMetrics
	RoleSourceArtifactIntegrity *RoleSourceArtifactIntegrityMetrics
	RoleSourceRetention         *RoleSourceRetentionMetrics
	ChannelLease                *ChannelLeaseMetrics
	SeatCapacity                *SeatCapacityMetrics
	// Sampler is non-nil only when RegistryOptions.BusinessSampler was
	// supplied with a valid Pool. Exposed so the cmd/server entrypoint
	// can plumb the same instance into health checks if it ever wants to.
	Sampler *BusinessSamplerCollector
=======
	Gatherer     prometheus.Gatherer
	HTTP         *HTTPMetrics
	Business     *BusinessMetrics
	ChannelMedia *ChannelMediaReconcilerMetrics
	ChannelLease *ChannelLeaseMetrics
	Wecom        *WecomMetrics
	DBRouting    *DBRoutingMetrics
>>>>>>> upstream/main
}

func NewRegistry(opts RegistryOptions) *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "multica_build_info",
		Help: "Build information for the Multica server binary.",
	}, []string{"version", "commit"})
	buildInfo.WithLabelValues(defaultLabel(opts.Version, "dev"), defaultLabel(opts.Commit, "unknown")).Set(1)
	reg.MustRegister(buildInfo)

	httpMetrics := NewHTTPMetrics()
	reg.MustRegister(httpMetrics.Collectors()...)

	businessMetrics := NewBusinessMetrics()
	reg.MustRegister(businessMetrics.Collectors()...)

	channelMedia := NewChannelMediaReconcilerMetrics()
	reg.MustRegister(channelMedia.Collectors()...)
	channelDelivery := NewChannelDeliveryMetrics()
	reg.MustRegister(channelDelivery.Collectors()...)

	channelLease := NewChannelLeaseMetrics()
	reg.MustRegister(channelLease.Collectors()...)

	wecomMetrics := NewWecomMetrics()
	reg.MustRegister(wecomMetrics.Collectors()...)
	dbRoutingMetrics := NewDBRoutingMetrics()
	reg.MustRegister(dbRoutingMetrics.Collectors()...)

	roleSourceMetrics := NewRoleSourceMetrics()
	reg.MustRegister(roleSourceMetrics.Collectors()...)
	roleSourceArtifactGC := NewRoleSourceArtifactGCMetrics()
	reg.MustRegister(roleSourceArtifactGC.Collectors()...)
	roleSourceArtifactIntegrity := NewRoleSourceArtifactIntegrityMetrics()
	reg.MustRegister(roleSourceArtifactIntegrity.Collectors()...)
	roleSourceRetention := NewRoleSourceRetentionMetrics()
	reg.MustRegister(roleSourceRetention.Collectors()...)

	if opts.Pool != nil {
		reg.MustRegister(NewDBCollector(opts.Pool, opts.ReplicaPool))
	}
	if opts.Realtime != nil {
		reg.MustRegister(NewRealtimeCollector(opts.Realtime))
	}
	if opts.DaemonWS != nil {
		reg.MustRegister(NewDaemonWSCollector(opts.DaemonWS))
	}

	return &Registry{
<<<<<<< HEAD
		Gatherer:                    reg,
		HTTP:                        httpMetrics,
		Business:                    businessMetrics,
		ChannelMedia:                channelMedia,
		ChannelDelivery:             channelDelivery,
		Wecom:                       wecomMetrics,
		RoleSource:                  roleSourceMetrics,
		RoleSourceArtifactGC:        roleSourceArtifactGC,
		RoleSourceArtifactIntegrity: roleSourceArtifactIntegrity,
		RoleSourceRetention:         roleSourceRetention,
		ChannelLease:                channelLease,
		SeatCapacity:                seatCapacity,
		Sampler:                     sampler,
=======
		Gatherer:     reg,
		HTTP:         httpMetrics,
		Business:     businessMetrics,
		ChannelMedia: channelMedia,
		ChannelLease: channelLease,
		Wecom:        wecomMetrics,
		DBRouting:    dbRoutingMetrics,
>>>>>>> upstream/main
	}
}

func defaultLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
