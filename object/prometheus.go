// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"fmt"
	"time"

	metric "github.com/luxfi/metric"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	io_prometheus_client "github.com/prometheus/client_model/go"
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
	// ApiThroughput uses *prometheus.GaugeVec directly because Reset() is needed
	ApiThroughput = promauto.NewGaugeVec(metric.GaugeOpts{
		Name: "cloud_api_throughput",
		Help: "The throughput of each api access",
	}, []string{"path", "method"})

	ApiLatency = promauto.NewHistogramVec(metric.HistogramOpts{
		Name: "cloud_api_latency",
		Help: "API processing latency in milliseconds",
	}, []string{"path", "method"})

	CpuUsage = promauto.NewGaugeVec(metric.GaugeOpts{
		Name: "cloud_cpu_usage",
		Help: "Hanzo Cloud cpu usage",
	}, []string{"cpuNum"})

	MemoryUsage = promauto.NewGaugeVec(metric.GaugeOpts{
		Name: "cloud_memory_usage",
		Help: "Hanzo Cloud memory usage in Byte",
	}, []string{"type"})

	TotalThroughput = promauto.NewGauge(metric.GaugeOpts{
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
	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return nil, err
	}
	for _, metricFamily := range metricFamilies {
		switch metricFamily.GetName() {
		case "cloud_api_throughput":
			res.ApiThroughput = getGaugeVecInfo(metricFamily)
		case "cloud_api_latency":
			res.ApiLatency = getHistogramVecInfo(metricFamily)
		case "cloud_total_throughput":
			res.TotalThroughput = metricFamily.GetMetric()[0].GetGauge().GetValue()
		}
	}

	return res, nil
}

func getHistogramVecInfo(metricFamily *io_prometheus_client.MetricFamily) []HistogramVecInfo {
	var histogramVecInfos []HistogramVecInfo
	for _, metric := range metricFamily.GetMetric() {
		sampleCount := metric.GetHistogram().GetSampleCount()
		sampleSum := metric.GetHistogram().GetSampleSum()
		latency := sampleSum / float64(sampleCount)
		histogramVecInfo := HistogramVecInfo{
			Method:  metric.Label[0].GetValue(),
			Name:    metric.Label[1].GetValue(),
			Count:   sampleCount,
			Latency: fmt.Sprintf("%.3f", latency),
		}
		histogramVecInfos = append(histogramVecInfos, histogramVecInfo)
	}
	return histogramVecInfos
}

func getGaugeVecInfo(metricFamily *io_prometheus_client.MetricFamily) []GaugeVecInfo {
	var counterVecInfos []GaugeVecInfo
	for _, metric := range metricFamily.GetMetric() {
		counterVecInfo := GaugeVecInfo{
			Method:     metric.Label[0].GetValue(),
			Name:       metric.Label[1].GetValue(),
			Throughput: metric.Gauge.GetValue(),
		}
		counterVecInfos = append(counterVecInfos, counterVecInfo)
	}
	return counterVecInfos
}
