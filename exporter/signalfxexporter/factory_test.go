// Copyright 2019, OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package signalfxexporter

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"os"
	"testing"
	"time"

	agentmetricspb "github.com/census-instrumentation/opencensus-proto/gen-go/agent/metrics/v1"
	metricspb "github.com/census-instrumentation/opencensus-proto/gen-go/metrics/v1"
	sfxpb "github.com/signalfx/com_signalfx_metrics_protobuf/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config"
	"go.opentelemetry.io/collector/config/configcheck"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	"go.opentelemetry.io/collector/model/pdata"
	"go.opentelemetry.io/collector/translator/internaldata"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/signalfxexporter/internal/translation"
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/signalfxexporter/internal/translation/dpfilters"
)

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()
	assert.NotNil(t, cfg, "failed to create default config")
	assert.NoError(t, configcheck.ValidateConfig(cfg))
}

func TestCreateMetricsExporter(t *testing.T) {
	cfg := createDefaultConfig()
	c := cfg.(*Config)
	c.AccessToken = "access_token"
	c.Realm = "us0"

	_, err := createMetricsExporter(context.Background(), componenttest.NewNopExporterCreateSettings(), cfg)
	assert.NoError(t, err)
}

func TestCreateTracesExporter(t *testing.T) {
	cfg := createDefaultConfig()
	c := cfg.(*Config)
	c.AccessToken = "access_token"
	c.Realm = "us0"

	_, err := createTracesExporter(context.Background(), componenttest.NewNopExporterCreateSettings(), cfg)
	assert.NoError(t, err)
}

func TestCreateTracesExporterNoAccessToken(t *testing.T) {
	cfg := createDefaultConfig()
	c := cfg.(*Config)
	c.Realm = "us0"

	_, err := createTracesExporter(context.Background(), componenttest.NewNopExporterCreateSettings(), cfg)
	assert.EqualError(t, err, "access_token is required")
}

func TestCreateInstanceViaFactory(t *testing.T) {
	factory := NewFactory()

	cfg := factory.CreateDefaultConfig()
	c := cfg.(*Config)
	c.AccessToken = "access_token"
	c.Realm = "us0"

	exp, err := factory.CreateMetricsExporter(
		context.Background(),
		componenttest.NewNopExporterCreateSettings(),
		cfg)
	assert.NoError(t, err)
	assert.NotNil(t, exp)

	// Set values that don't have a valid default.
	expCfg := cfg.(*Config)
	expCfg.AccessToken = "testToken"
	expCfg.Realm = "us1"
	exp, err = factory.CreateMetricsExporter(
		context.Background(),
		componenttest.NewNopExporterCreateSettings(),
		cfg)
	assert.NoError(t, err)
	require.NotNil(t, exp)

	logExp, err := factory.CreateLogsExporter(
		context.Background(),
		componenttest.NewNopExporterCreateSettings(),
		cfg)
	assert.NoError(t, err)
	require.NotNil(t, logExp)

	assert.NoError(t, exp.Shutdown(context.Background()))
}

func TestCreateMetricsExporter_CustomConfig(t *testing.T) {
	config := &Config{
		ExporterSettings: config.NewExporterSettings(config.NewID(typeStr)),
		AccessToken:      "testToken",
		Realm:            "us1",
		Headers: map[string]string{
			"added-entry": "added value",
			"dot.test":    "test",
		},
		TimeoutSettings: exporterhelper.TimeoutSettings{Timeout: 2 * time.Second},
	}

	te, err := createMetricsExporter(context.Background(), componenttest.NewNopExporterCreateSettings(), config)
	assert.NoError(t, err)
	assert.NotNil(t, te)
}

func TestFactory_CreateMetricsExporterFails(t *testing.T) {
	tests := []struct {
		name         string
		config       *Config
		errorMessage string
	}{
		{
			name: "negative_duration",
			config: &Config{
				ExporterSettings: config.NewExporterSettings(config.NewID(typeStr)),
				AccessToken:      "testToken",
				Realm:            "lab",
				TimeoutSettings:  exporterhelper.TimeoutSettings{Timeout: -2 * time.Second},
			},
			errorMessage: "failed to process \"signalfx\" config: cannot have a negative \"timeout\"",
		},
		{
			name: "empty_realm_and_urls",
			config: &Config{
				ExporterSettings: config.NewExporterSettings(config.NewID(typeStr)),
				AccessToken:      "testToken",
			},
			errorMessage: "failed to process \"signalfx\" config: requires a non-empty \"realm\"," +
				" or \"ingest_url\" and \"api_url\" should be explicitly set",
		},
		{
			name: "empty_realm_and_api_url",
			config: &Config{
				ExporterSettings: config.NewExporterSettings(config.NewID(typeStr)),
				AccessToken:      "testToken",
				IngestURL:        "http://localhost:123",
			},
			errorMessage: "failed to process \"signalfx\" config: requires a non-empty \"realm\"," +
				" or \"ingest_url\" and \"api_url\" should be explicitly set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te, err := createMetricsExporter(context.Background(), componenttest.NewNopExporterCreateSettings(), tt.config)
			assert.EqualError(t, err, tt.errorMessage)
			assert.Nil(t, te)
		})
	}
}

func TestDefaultTranslationRules(t *testing.T) {
	rules, err := loadDefaultTranslationRules()
	require.NoError(t, err)
	require.NotNil(t, rules, "rules are nil")
	tr, err := translation.NewMetricTranslator(rules, 1)
	require.NoError(t, err)
	data := testMetricsData()

	c, err := translation.NewMetricsConverter(zap.NewNop(), tr, nil, nil, "")
	require.NoError(t, err)
	translated := c.MetricDataToSignalFxV2(data)
	require.NotNil(t, translated)

	metrics := make(map[string][]*sfxpb.DataPoint)
	for _, pt := range translated {
		if _, ok := metrics[pt.Metric]; !ok {
			metrics[pt.Metric] = make([]*sfxpb.DataPoint, 0, 1)
		}
		metrics[pt.Metric] = append(metrics[pt.Metric], pt)
	}

	// memory.utilization new metric calculation
	dps, ok := metrics["memory.utilization"]
	require.True(t, ok, "memory.utilization metric not found")
	require.Equal(t, 1, len(dps))
	require.Equal(t, 40.0, *dps[0].Value.DoubleValue)

	// system.network.operations.total new metric calculation
	dps, ok = metrics["system.disk.operations.total"]
	require.True(t, ok, "system.network.operations.total metrics not found")
	require.Equal(t, 4, len(dps))
	require.Equal(t, 2, len(dps[0].Dimensions))

	// system.network.io.total new metric calculation
	dps, ok = metrics["system.disk.io.total"]
	require.True(t, ok, "system.network.io.total metrics not found")
	require.Equal(t, 2, len(dps))
	require.Equal(t, 2, len(dps[0].Dimensions))
	for _, dp := range dps {
		require.Equal(t, "direction", dp.Dimensions[1].Key)
		switch dp.Dimensions[1].Value {
		case "write":
			require.Equal(t, int64(11e9), *dp.Value.IntValue)
		case "read":
			require.Equal(t, int64(3e9), *dp.Value.IntValue)
		}
	}

	// disk_ops.total gauge from system.disk.operations cumulative, where is disk_ops.total
	// is the cumulative across devices and directions.
	dps, ok = metrics["disk_ops.total"]
	require.True(t, ok, "disk_ops.total metrics not found")
	require.Equal(t, 1, len(dps))
	require.Equal(t, int64(8e3), *dps[0].Value.IntValue)
	require.Equal(t, 1, len(dps[0].Dimensions))
	require.Equal(t, "host", dps[0].Dimensions[0].Key)
	require.Equal(t, "host0", dps[0].Dimensions[0].Value)

	// system.network.io.total new metric calculation
	dps, ok = metrics["system.network.io.total"]
	require.True(t, ok, "system.network.io.total metrics not found")
	require.Equal(t, 2, len(dps))
	require.Equal(t, 4, len(dps[0].Dimensions))

	// system.network.packets.total new metric calculation
	dps, ok = metrics["system.network.packets.total"]
	require.True(t, ok, "system.network.packets.total metrics not found")
	require.Equal(t, 1, len(dps))
	require.Equal(t, 4, len(dps[0].Dimensions))
	require.Equal(t, int64(350), *dps[0].Value.IntValue)
	require.Equal(t, "direction", dps[0].Dimensions[0].Key)
	require.Equal(t, "receive", dps[0].Dimensions[0].Value)

	// network.total new metric calculation
	dps, ok = metrics["network.total"]
	require.True(t, ok, "network.total metrics not found")
	require.Equal(t, 1, len(dps))
	require.Equal(t, 3, len(dps[0].Dimensions))
	require.Equal(t, int64(10e9), *dps[0].Value.IntValue)
}

func TestCreateMetricsExporterWithDefaultExcludeMetrics(t *testing.T) {
	config := &Config{
		ExporterSettings: config.NewExporterSettings(config.NewID(typeStr)),
		AccessToken:      "testToken",
		Realm:            "us1",
	}

	te, err := createMetricsExporter(context.Background(), componenttest.NewNopExporterCreateSettings(), config)
	require.NoError(t, err)
	require.NotNil(t, te)

	// Validate that default excludes are always loaded.
	assert.Equal(t, 11, len(config.ExcludeMetrics))
}

func TestCreateMetricsExporterWithExcludeMetrics(t *testing.T) {
	config := &Config{
		ExporterSettings: config.NewExporterSettings(config.NewID(typeStr)),
		AccessToken:      "testToken",
		Realm:            "us1",
		ExcludeMetrics: []dpfilters.MetricFilter{
			{
				MetricNames: []string{"metric1"},
			},
		},
	}

	te, err := createMetricsExporter(context.Background(), componenttest.NewNopExporterCreateSettings(), config)
	require.NoError(t, err)
	require.NotNil(t, te)

	// Validate that default excludes are always loaded.
	assert.Equal(t, 12, len(config.ExcludeMetrics))
}

func TestCreateMetricsExporterWithEmptyExcludeMetrics(t *testing.T) {
	config := &Config{
		ExporterSettings: config.NewExporterSettings(config.NewID(typeStr)),
		AccessToken:      "testToken",
		Realm:            "us1",
		ExcludeMetrics:   []dpfilters.MetricFilter{},
	}

	te, err := createMetricsExporter(context.Background(), componenttest.NewNopExporterCreateSettings(), config)
	require.NoError(t, err)
	require.NotNil(t, te)

	// Validate that default excludes are overridden when exclude metrics
	// is explicitly set to an empty slice.
	assert.Equal(t, 0, len(config.ExcludeMetrics))
}

func testMetricsData() pdata.ResourceMetrics {
	md := agentmetricspb.ExportMetricsServiceRequest{
		Metrics: []*metricspb.Metric{
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name:        "system.memory.usage",
					Description: "Bytes of memory in use",
					Unit:        "bytes",
					Type:        metricspb.MetricDescriptor_GAUGE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "state"},
						{Key: "host"},
						{Key: "kubernetes_node"},
						{Key: "kubernetes_cluster"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "used",
							HasValue: true,
						}, {
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 4e9,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "free",
							HasValue: true,
						}, {
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 6e9,
							},
						}},
					},
				},
			},
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name:        "system.disk.io",
					Description: "Disk I/O.",
					Type:        metricspb.MetricDescriptor_CUMULATIVE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "host"},
						{Key: "direction"},
						{Key: "device"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "read",
							HasValue: true,
						}, {
							Value:    "sda1",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 1e9,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "read",
							HasValue: true,
						}, {
							Value:    "sda2",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 2e9,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "write",
							HasValue: true,
						}, {
							Value:    "sda1",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 3e9,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "write",
							HasValue: true,
						}, {
							Value:    "sda2",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 8e9,
							},
						}},
					},
				},
			},
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name:        "system.disk.operations",
					Description: "Disk operations count.",
					Unit:        "bytes",
					Type:        metricspb.MetricDescriptor_CUMULATIVE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "host"},
						{Key: "direction"},
						{Key: "device"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "read",
							HasValue: true,
						}, {
							Value:    "sda1",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 4e3,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "read",
							HasValue: true,
						}, {
							Value:    "sda2",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 6e3,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "write",
							HasValue: true,
						}, {
							Value:    "sda1",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 1e3,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "write",
							HasValue: true,
						}, {
							Value:    "sda2",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 5e3,
							},
						}},
					},
				},
			},
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name:        "system.disk.operations",
					Description: "Disk operations count.",
					Unit:        "bytes",
					Type:        metricspb.MetricDescriptor_CUMULATIVE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "host"},
						{Key: "direction"},
						{Key: "device"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "read",
							HasValue: true,
						}, {
							Value:    "sda1",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000060,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 6e3,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "read",
							HasValue: true,
						}, {
							Value:    "sda2",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000060,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 8e3,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "write",
							HasValue: true,
						}, {
							Value:    "sda1",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000060,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 3e3,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "write",
							HasValue: true,
						}, {
							Value:    "sda2",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000060,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 7e3,
							},
						}},
					},
				},
			},
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name:        "system.network.io",
					Description: "The number of bytes transmitted and received",
					Unit:        "bytes",
					Type:        metricspb.MetricDescriptor_GAUGE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "direction"},
						{Key: "device"},
						{Key: "host"},
						{Key: "kubernetes_node"},
						{Key: "kubernetes_cluster"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "receive",
							HasValue: true,
						}, {
							Value:    "eth0",
							HasValue: true,
						}, {
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 4e9,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "transmit",
							HasValue: true,
						}, {
							Value:    "eth0",
							HasValue: true,
						}, {
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 6e9,
							},
						}},
					},
				},
			},
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name:        "system.network.packets",
					Description: "The number of packets transferred",
					Type:        metricspb.MetricDescriptor_GAUGE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "direction"},
						{Key: "device"},
						{Key: "host"},
						{Key: "kubernetes_node"},
						{Key: "kubernetes_cluster"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "receive",
							HasValue: true,
						}, {
							Value:    "eth0",
							HasValue: true,
						}, {
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 200,
							},
						}},
					},
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "receive",
							HasValue: true,
						}, {
							Value:    "eth1",
							HasValue: true,
						}, {
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 150,
							},
						}},
					},
				},
			},
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name: "container.memory.working_set",
					Unit: "bytes",
					Type: metricspb.MetricDescriptor_GAUGE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "host"},
						{Key: "kubernetes_node"},
						{Key: "kubernetes_cluster"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 1000,
							},
						}},
					},
				},
			},
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name: "container.memory.page_faults",
					Unit: "",
					Type: metricspb.MetricDescriptor_GAUGE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "host"},
						{Key: "kubernetes_node"},
						{Key: "kubernetes_cluster"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 1000,
							},
						}},
					},
				},
			},
			{
				MetricDescriptor: &metricspb.MetricDescriptor{
					Name: "container.memory.major_page_faults",
					Unit: "",
					Type: metricspb.MetricDescriptor_GAUGE_INT64,
					LabelKeys: []*metricspb.LabelKey{
						{Key: "host"},
						{Key: "kubernetes_node"},
						{Key: "kubernetes_cluster"},
					},
				},
				Timeseries: []*metricspb.TimeSeries{
					{
						StartTimestamp: &timestamppb.Timestamp{},
						LabelValues: []*metricspb.LabelValue{{
							Value:    "host0",
							HasValue: true,
						}, {
							Value:    "node0",
							HasValue: true,
						}, {
							Value:    "cluster0",
							HasValue: true,
						}},
						Points: []*metricspb.Point{{
							Timestamp: &timestamppb.Timestamp{
								Seconds: 1596000000,
							},
							Value: &metricspb.Point_Int64Value{
								Int64Value: 1000,
							},
						}},
					},
				},
			},
		},
	}
	return internaldata.OCToMetrics(md.Node, md.Resource, md.Metrics).ResourceMetrics().At(0)
}

func TestDefaultDiskTranslations(t *testing.T) {
	var pts []*sfxpb.DataPoint
	err := testReadJSON("testdata/json/system.filesystem.usage.json", &pts)
	require.NoError(t, err)

	tr := testGetTranslator(t)
	translated := tr.TranslateDataPoints(zap.NewNop(), pts)
	require.NotNil(t, translated)

	m := map[string][]*sfxpb.DataPoint{}
	for _, pt := range translated {
		l := m[pt.Metric]
		l = append(l, pt)
		m[pt.Metric] = l
	}

	_, ok := m["disk.total"]
	require.False(t, ok)

	_, ok = m["disk.summary_total"]
	require.False(t, ok)

	_, ok = m["df_complex.used_total"]
	require.False(t, ok)

	du, ok := m["disk.utilization"]
	require.True(t, ok)
	require.Equal(t, 4, len(du[0].Dimensions))
	// cheap test for pct conversion
	require.True(t, *du[0].Value.DoubleValue > 1)

	dsu, ok := m["disk.summary_utilization"]
	require.True(t, ok)
	require.Equal(t, 3, len(dsu[0].Dimensions))
	require.True(t, *dsu[0].Value.DoubleValue > 1)
}

func testGetTranslator(t *testing.T) *translation.MetricTranslator {
	rules, err := loadDefaultTranslationRules()
	require.NoError(t, err)
	require.NotNil(t, rules, "rules are nil")
	tr, err := translation.NewMetricTranslator(rules, 3600)
	require.NoError(t, err)
	return tr
}

func TestDefaultCPUTranslations(t *testing.T) {
	var pts1 []*sfxpb.DataPoint
	err := testReadJSON("testdata/json/system.cpu.time.1.json", &pts1)
	require.NoError(t, err)

	var pts2 []*sfxpb.DataPoint
	err = testReadJSON("testdata/json/system.cpu.time.2.json", &pts2)
	require.NoError(t, err)

	tr := testGetTranslator(t)
	log := zap.NewNop()

	// write 'prev' points from which to calculate deltas
	_ = tr.TranslateDataPoints(log, pts1)

	// calculate cpu utilization
	translated2 := tr.TranslateDataPoints(log, pts2)

	m := map[string][]*sfxpb.DataPoint{}
	for _, pt := range translated2 {
		pts := m[pt.Metric]
		pts = append(pts, pt)
		m[pt.Metric] = pts
	}

	cpuUtil := m["cpu.utilization"]
	require.Equal(t, 1, len(cpuUtil))
	for _, pt := range cpuUtil {
		require.Equal(t, 66, int(*pt.Value.DoubleValue))
	}
}

func TestDefaultExcludes_translated(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	setDefaultExcludes(cfg)

	converter, err := translation.NewMetricsConverter(zap.NewNop(), testGetTranslator(t), cfg.ExcludeMetrics, cfg.IncludeMetrics, "")
	require.NoError(t, err)

	var metrics []map[string]string
	err = testReadJSON("testdata/json/non_default_metrics.json", &metrics)
	require.NoError(t, err)

	rms := getResourceMetrics(metrics)
	require.Equal(t, 9, rms.InstrumentationLibraryMetrics().At(0).Metrics().Len())
	dps := converter.MetricDataToSignalFxV2(rms)

	// the default cpu.utilization metric is added after applying the default translations
	// (because cpu.utilization_per_core is supplied) and should not be excluded
	require.Equal(t, 1, len(dps))
	require.Equal(t, "cpu.utilization", dps[0].Metric)

}

func TestDefaultExcludes_not_translated(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	setDefaultExcludes(cfg)

	converter, err := translation.NewMetricsConverter(zap.NewNop(), nil, cfg.ExcludeMetrics, cfg.IncludeMetrics, "")
	require.NoError(t, err)

	var metrics []map[string]string
	err = testReadJSON("testdata/json/non_default_metrics_otel_convention.json", &metrics)
	require.NoError(t, err)

	rms := getResourceMetrics(metrics)
	require.Equal(t, 71, rms.InstrumentationLibraryMetrics().At(0).Metrics().Len())
	dps := converter.MetricDataToSignalFxV2(rms)
	require.Equal(t, 0, len(dps))
}

func getResourceMetrics(metrics []map[string]string) pdata.ResourceMetrics {
	rms := pdata.NewResourceMetrics()
	ilms := rms.InstrumentationLibraryMetrics().AppendEmpty()
	ilms.Metrics().Resize(len(metrics))

	for i, mp := range metrics {
		m := ilms.Metrics().At(i)
		// Set data type to some arbitrary since it does not matter for this test.
		m.SetDataType(pdata.MetricDataTypeIntSum)
		dp := m.IntSum().DataPoints().AppendEmpty()
		dp.SetValue(0)
		labelsMap := dp.LabelsMap()
		for k, v := range mp {
			if v == "" {
				m.SetName(k)
				continue
			}
			labelsMap.Insert(k, v)
		}
	}
	return rms
}

func testReadJSON(f string, v interface{}) error {
	file, err := os.Open(f)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, &v)
}
