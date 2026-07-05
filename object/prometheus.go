// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"fmt"
	"net/http"
	"time"

	metric "github.com/luxfi/metric"
)

type PrometheusInfo struct {
	ApiThroughput   []GaugeVecInfo     `json:"apiThroughput"`
	ApiLatency      []HistogramVecInfo `json:"apiLatency"`
	TotalThroughput float64            `json:"totalThroughput"`
}
type GaugeVecInfo struct {
	Method     string  `json:"method"`
	Name       string  `json:"name"`
	Throughput float64 `json:"throughput"`
}
type HistogramVecInfo struct {
	Method  string `json:"method"`
	Name    string `json:"name"`
	Count   uint64 `json:"count"`
	Latency string `json:"latency"`
}

var (
	// ApiThroughput uses *metric.GaugeVec directly because Reset() is needed
	ApiThroughput = metric.NewGaugeVec(metric.GaugeOpts{
		Name: "cloud_api_throughput",
		Help: "The throughput of each api access",
	}, []string{"path", "method"})
	ApiLatency = metric.NewHistogramVec(metric.HistogramOpts{
		Name: "cloud_api_latency",
		Help: "API processing latency in milliseconds",
	}, []string{"path", "method"})
	CpuUsage = metric.NewGaugeVec(metric.GaugeOpts{
		Name: "cloud_cpu_usage",
		Help: "Hanzo Cloud cpu usage",
	}, []string{"cpuNum"})
	MemoryUsage = metric.NewGaugeVec(metric.GaugeOpts{
		Name: "cloud_memory_usage",
		Help: "Hanzo Cloud memory usage in Byte",
	}, []string{"type"})
	TotalThroughput = metric.NewGauge(metric.GaugeOpts{
		Name: "cloud_total_throughput",
		Help: "The total throughput of Hanzo Cloud",
	})
)

func ClearThroughputPerSecond() {
	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		ApiThroughput.Reset()
		TotalThroughput.Set(0)
	}
}

func GetPrometheusInfo() (*PrometheusInfo, error) {
	res := &PrometheusInfo{}
	metricFamilies, err := metric.DefaultRegistry.Gather()
	if err != nil {
		return nil, err
	}
	for _, metricFamily := range metricFamilies {
		switch metricFamily.Name {
		case "cloud_api_throughput":
			res.ApiThroughput = getGaugeVecInfo(metricFamily)
		case "cloud_api_latency":
			res.ApiLatency = getHistogramVecInfo(metricFamily)
		case "cloud_total_throughput":
			if len(metricFamily.Metrics) > 0 {
				res.TotalThroughput = metricFamily.Metrics[0].Value.Value
			}
		}
	}
	return res, nil
}

// MetricsHandler returns the Prometheus text-exposition handler for GET
// /v1/metrics, bound to the SAME DefaultRegistry the metric vars above register
// into (the package-level metric.NewGaugeVec / NewHistogramVec helpers delegate
// to DefaultRegistry). This must NOT use metric.Handler(): that convenience
// wrapper binds a fresh, throwaway registry, so it exposes an EMPTY scrape
// regardless of what has been recorded — the endpoint would answer 200 with a
// zero-length body forever. Binding DefaultRegistry mirrors GetPrometheusInfo,
// which reads the same registry: one registry, one source of truth.
func MetricsHandler() http.Handler {
	return metric.HandlerFor(metric.DefaultRegistry)
}

func getHistogramVecInfo(metricFamily *metric.MetricFamily) []HistogramVecInfo {
	var histogramVecInfos []HistogramVecInfo
	for _, m := range metricFamily.Metrics {
		sampleCount := m.Value.SampleCount
		sampleSum := m.Value.SampleSum
		latency := sampleSum / float64(sampleCount)
		histogramVecInfo := HistogramVecInfo{
			Method:  labelValue(m.Labels, 0),
			Name:    labelValue(m.Labels, 1),
			Count:   sampleCount,
			Latency: fmt.Sprintf("%.3f", latency),
		}
		histogramVecInfos = append(histogramVecInfos, histogramVecInfo)
	}
	return histogramVecInfos
}

func getGaugeVecInfo(metricFamily *metric.MetricFamily) []GaugeVecInfo {
	var counterVecInfos []GaugeVecInfo
	for _, m := range metricFamily.Metrics {
		counterVecInfo := GaugeVecInfo{
			Method:     labelValue(m.Labels, 0),
			Name:       labelValue(m.Labels, 1),
			Throughput: m.Value.Value,
		}
		counterVecInfos = append(counterVecInfos, counterVecInfo)
	}
	return counterVecInfos
}

func labelValue(labels []metric.LabelPair, i int) string {
	if i < 0 || i >= len(labels) {
		return ""
	}
	return labels[i].Value
}
