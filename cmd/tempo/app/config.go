package app

import (
	"flag"
	"fmt"
	"time"

	"github.com/grafana/dskit/flagext"
	"github.com/grafana/dskit/kv/memberlist"
	"github.com/grafana/tempo/modules/compactor"
	"github.com/grafana/tempo/modules/distributor"
	"github.com/grafana/tempo/modules/frontend"
	"github.com/grafana/tempo/modules/generator"
	generator_client "github.com/grafana/tempo/modules/generator/client"
	"github.com/grafana/tempo/modules/ingester"
	ingester_client "github.com/grafana/tempo/modules/ingester/client"
	"github.com/grafana/tempo/modules/overrides"
	"github.com/grafana/tempo/modules/querier"
	"github.com/grafana/tempo/modules/storage"
	internalserver "github.com/grafana/tempo/pkg/server"
	"github.com/grafana/tempo/pkg/usagestats"
	"github.com/grafana/tempo/pkg/util"
	"github.com/grafana/tempo/tempodb"
	"github.com/grafana/tempo/tempodb/encoding/common"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/weaveworks/common/server"
)

// Config is the root config for App.
type Config struct {
	Target                       string `yaml:"target,omitempty"`
	AuthEnabled                  bool   `yaml:"auth_enabled,omitempty"`
	MultitenancyEnabled          bool   `yaml:"multitenancy_enabled,omitempty"`
	HTTPAPIPrefix                string `yaml:"http_api_prefix"`
	UseOTelTracer                bool   `yaml:"use_otel_tracer,omitempty"`
	EnableGoRuntimeMetrics       bool   `yaml:"enable_go_runtime_metrics,omitempty"`
	AutocompleteFilteringEnabled bool   `yaml:"autocomplete_filtering_enabled,omitempty"`

	Server          server.Config           `yaml:"server,omitempty"`
	InternalServer  internalserver.Config   `yaml:"internal_server,omitempty"`
	Distributor     distributor.Config      `yaml:"distributor,omitempty"`
	IngesterClient  ingester_client.Config  `yaml:"ingester_client,omitempty"`
	GeneratorClient generator_client.Config `yaml:"metrics_generator_client,omitempty"`
	Querier         querier.Config          `yaml:"querier,omitempty"`
	Frontend        frontend.Config         `yaml:"query_frontend,omitempty"`
	Compactor       compactor.Config        `yaml:"compactor,omitempty"`
	Ingester        ingester.Config         `yaml:"ingester,omitempty"`
	Generator       generator.Config        `yaml:"metrics_generator,omitempty"`
	StorageConfig   storage.Config          `yaml:"storage,omitempty"`
	LimitsConfig    overrides.Limits        `yaml:"overrides,omitempty"`
	MemberlistKV    memberlist.KVConfig     `yaml:"memberlist,omitempty"`
	UsageReport     usagestats.Config       `yaml:"usage_report,omitempty"`

	// This is used by applications hosting Tempo to disable the default behavior
	// of routing grpc over the main http server. Specifically this is for
	// Grafana Enterprise Traces gateway module which does its own protocol muxing.
	DoNotRouteHTTPToGRPC bool `yaml:"-"`
}

func newDefaultConfig() *Config {
	defaultConfig := &Config{}
	defaultFS := flag.NewFlagSet("", flag.PanicOnError)
	defaultConfig.RegisterFlagsAndApplyDefaults("", defaultFS)
	return defaultConfig
}

// RegisterFlagsAndApplyDefaults registers flag.
func (c *Config) RegisterFlagsAndApplyDefaults(prefix string, f *flag.FlagSet) {
	c.Target = SingleBinary
	// global settings
	f.StringVar(&c.Target, "target", SingleBinary, "target module")
	f.BoolVar(&c.AuthEnabled, "auth.enabled", false, "Set to true to enable auth (deprecated: use multitenancy.enabled)")
	f.BoolVar(&c.MultitenancyEnabled, "multitenancy.enabled", false, "Set to true to enable multitenancy.")
	f.StringVar(&c.HTTPAPIPrefix, "http-api-prefix", "", "String prefix for all http api endpoints.")
	f.BoolVar(&c.UseOTelTracer, "use-otel-tracer", false, "Set to true to replace the OpenTracing tracer with the OpenTelemetry tracer")
	f.BoolVar(&c.EnableGoRuntimeMetrics, "enable-go-runtime-metrics", false, "Set to true to enable all Go runtime metrics")
	f.BoolVar(&c.AutocompleteFilteringEnabled, "autocomplete-filtering.enabled", false, "Set to true to enable autocomplete filtering")

	// Server settings
	flagext.DefaultValues(&c.Server)
	c.Server.LogLevel.RegisterFlags(f)

	// Internal server settings
	flagext.DefaultValues(&c.InternalServer)

	// Increase max message size to 16MB
	c.Server.GPRCServerMaxRecvMsgSize = 16 * 1024 * 1024
	c.Server.GRPCServerMaxSendMsgSize = 16 * 1024 * 1024

	// The following GRPC server settings are added to address this issue - https://github.com/grafana/tempo/issues/493
	// The settings prevent the grpc server from sending a GOAWAY message if a client sends heartbeat messages
	// too frequently (due to lack of real traffic).
	c.Server.GRPCServerMinTimeBetweenPings = 10 * time.Second
	c.Server.GRPCServerPingWithoutStreamAllowed = true

	f.IntVar(&c.Server.HTTPListenPort, "server.http-listen-port", 80, "HTTP server listen port.")
	f.IntVar(&c.Server.GRPCListenPort, "server.grpc-listen-port", 9095, "gRPC server listen port.")

	// Memberlist settings
	fs := flag.NewFlagSet("", flag.PanicOnError) // create a new flag set b/c we don't want all of the memberlist settings in our flags. we're just doing this to get defaults
	c.MemberlistKV.RegisterFlags(fs)
	_ = fs.Parse([]string{})
	// these defaults were chosen to balance resource usage vs. ring propagation speed. they are a "toned down" version of
	// the memberlist defaults
	c.MemberlistKV.RetransmitMult = 2
	c.MemberlistKV.GossipInterval = time.Second
	c.MemberlistKV.GossipNodes = 2
	c.MemberlistKV.EnableCompression = false

	f.Var(&c.MemberlistKV.JoinMembers, "memberlist.host-port", "Host port to connect to memberlist cluster.")
	f.IntVar(&c.MemberlistKV.TCPTransport.BindPort, "memberlist.bind-port", 7946, "Port for memberlist to communicate on")

	// Everything else
	flagext.DefaultValues(&c.IngesterClient)
	c.IngesterClient.GRPCClientConfig.GRPCCompression = "snappy"
	flagext.DefaultValues(&c.GeneratorClient)
	c.GeneratorClient.GRPCClientConfig.GRPCCompression = "snappy"
	flagext.DefaultValues(&c.LimitsConfig)

	c.Distributor.RegisterFlagsAndApplyDefaults(util.PrefixConfig(prefix, "distributor"), f)
	c.Ingester.RegisterFlagsAndApplyDefaults(util.PrefixConfig(prefix, "ingester"), f)
	c.Generator.RegisterFlagsAndApplyDefaults(util.PrefixConfig(prefix, "generator"), f)
	c.Querier.RegisterFlagsAndApplyDefaults(util.PrefixConfig(prefix, "querier"), f)
	c.Frontend.RegisterFlagsAndApplyDefaults(util.PrefixConfig(prefix, "frontend"), f)
	c.Compactor.RegisterFlagsAndApplyDefaults(util.PrefixConfig(prefix, "compactor"), f)
	c.StorageConfig.RegisterFlagsAndApplyDefaults(util.PrefixConfig(prefix, "storage"), f)
	c.UsageReport.RegisterFlagsAndApplyDefaults(util.PrefixConfig(prefix, "reporting"), f)
}

// MultitenancyIsEnabled checks if multitenancy is enabled
func (c *Config) MultitenancyIsEnabled() bool {
	return c.MultitenancyEnabled || c.AuthEnabled
}

// CheckConfig checks if config values are suspect and returns a bundled list of warnings and explanation.
func (c *Config) CheckConfig() []ConfigWarning {
	var warnings []ConfigWarning
	if c.Ingester.CompleteBlockTimeout < c.StorageConfig.Trace.BlocklistPoll {
		warnings = append(warnings, warnCompleteBlockTimeout)
	}

	if c.Compactor.Compactor.BlockRetention < c.StorageConfig.Trace.BlocklistPoll {
		warnings = append(warnings, warnBlockRetention)
	}

	if c.Compactor.Compactor.RetentionConcurrency == 0 {
		warnings = append(warnings, warnRetentionConcurrency)
	}

	if c.StorageConfig.Trace.Backend == "s3" && c.Compactor.Compactor.FlushSizeBytes < 5242880 {
		warnings = append(warnings, warnStorageTraceBackendS3)
	}

	if c.StorageConfig.Trace.BlocklistPollConcurrency == 0 {
		warnings = append(warnings, warnBlocklistPollConcurrency)
	}

	if c.Distributor.LogReceivedTraces {
		warnings = append(warnings, warnLogReceivedTraces)
	}

	if c.StorageConfig.Trace.Backend == "local" && c.Target != SingleBinary {
		warnings = append(warnings, warnStorageTraceBackendLocal)
	}

	// check v2 specific settings
	if c.StorageConfig.Trace.Block.Version != "v2" && c.StorageConfig.Trace.Block.IndexDownsampleBytes != common.DefaultIndexDownSampleBytes {
		warnings = append(warnings, newV2Warning("v2_index_downsample_bytes"))
	}

	if c.StorageConfig.Trace.Block.Version != "v2" && c.StorageConfig.Trace.Block.IndexPageSizeBytes != common.DefaultIndexPageSizeBytes {
		warnings = append(warnings, newV2Warning("v2_index_page_size_bytes"))
	}

	if c.StorageConfig.Trace.Block.Version != "v2" && c.Compactor.Compactor.ChunkSizeBytes != tempodb.DefaultChunkSizeBytes {
		warnings = append(warnings, newV2Warning("v2_in_buffer_bytes"))
	}

	if c.StorageConfig.Trace.Block.Version != "v2" && c.Compactor.Compactor.FlushSizeBytes != tempodb.DefaultFlushSizeBytes {
		warnings = append(warnings, newV2Warning("v2_out_buffer_bytes"))
	}

	if c.StorageConfig.Trace.Block.Version != "v2" && c.Compactor.Compactor.IteratorBufferSize != tempodb.DefaultIteratorBufferSize {
		warnings = append(warnings, newV2Warning("v2_prefetch_traces_count"))
	}

	return warnings
}

func (c *Config) Describe(ch chan<- *prometheus.Desc) {
	ch <- metricConfigFeatDesc
}

func (c *Config) Collect(ch chan<- prometheus.Metric) {

	features := map[string]int{
		"search_external_endpoints": 0,
	}

	if len(c.Querier.Search.ExternalEndpoints) > 0 {
		features["search_external_endpoints"] = 1
	}

	for label, value := range features {
		ch <- prometheus.MustNewConstMetric(metricConfigFeatDesc, prometheus.GaugeValue, float64(value), label)
	}
}

// ConfigWarning bundles message and explanation strings in one structure.
type ConfigWarning struct {
	Message string
	Explain string
}

var (
	warnCompleteBlockTimeout = ConfigWarning{
		Message: "ingester.complete_block_timeout < storage.trace.blocklist_poll",
		Explain: "You may receive 404s between the time the ingesters have flushed a trace and the querier is aware of the new block",
	}
	warnBlockRetention = ConfigWarning{
		Message: "compactor.compaction.compacted_block_timeout < storage.trace.blocklist_poll",
		Explain: "Queriers and Compactors may attempt to read a block that no longer exists",
	}
	warnRetentionConcurrency = ConfigWarning{
		Message: "c.Compactor.Compactor.RetentionConcurrency must be greater than zero. Using default.",
		Explain: fmt.Sprintf("default=%d", tempodb.DefaultRetentionConcurrency),
	}
	warnStorageTraceBackendS3 = ConfigWarning{
		Message: "c.Compactor.Compactor.FlushSizeBytes < 5242880",
		Explain: "Compaction flush size should be 5MB or higher for S3 backend",
	}
	warnBlocklistPollConcurrency = ConfigWarning{
		Message: "c.StorageConfig.Trace.BlocklistPollConcurrency must be greater than zero. Using default.",
		Explain: fmt.Sprintf("default=%d", tempodb.DefaultBlocklistPollConcurrency),
	}
	warnLogReceivedTraces = ConfigWarning{
		Message: "c.Distributor.LogReceivedTraces is deprecated. The new flag is c.Distributor.log_received_spans.enabled",
	}
	warnStorageTraceBackendLocal = ConfigWarning{
		Message: "Local backend will not correctly retrieve traces with a distributed deployment unless all components have access to the same disk. You should probably be using object storage as a backend.",
	}
)

func newV2Warning(setting string) ConfigWarning {
	return ConfigWarning{
		Message: "c.StorageConfig.Trace.Block.Version != \"v2\" but " + setting + " is set",
		Explain: "This setting is only used in v2 blocks",
	}
}
