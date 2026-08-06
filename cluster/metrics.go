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
package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/object"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func Describe(apps []*object.Application, lang string) {
	if len(apps) == 0 {
		return
	}
	if _, err := ensure(lang); err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, app := range apps {
		wg.Add(1)
		go func(app *object.Application) {
			defer wg.Done()
			if details, err := View(app.Namespace, lang); err == nil {
				app.Status = details.Status
				app.Details = details
				if app.URL == "" {
					if url := firstServiceURL(details.Services); url != "" {
						app.URL = url
					}
				}
			}
		}(app)
	}
	wg.Wait()
}

// URL retrieves the access URL for an application.
func URL(namespace string, lang string) (string, error) {
	c, err := ensure(lang)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	if url := firstServiceURL(getServices(ctx, c, namespace, getNodeIPs(ctx, c))); url != "" {
		return url, nil
	}
	return "", fmt.Errorf("%s", i18n.Translate(lang, "object:no accessible URL found for application"))
}

// firstServiceURL returns the first externally reachable service URL in the
// view, or "" when nothing is reachable. Pure — Describe reuses the view it has
// already assembled rather than re-reading the cluster per application.
func firstServiceURL(services []object.ServiceDetail) string {
	for _, service := range services {
		for _, port := range service.Ports {
			if port.URL != "" {
				return port.URL
			}
		}
	}
	return ""
}

// findIngressURL finds the external access URL for a service in Ingress rules.
func findIngressURL(serviceName string, servicePort int32, ingresses []networkingv1.Ingress) string {
	for _, ingress := range ingresses {
		// Iterate through Ingress rules
		for _, rule := range ingress.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			// Build the hostname
			host := rule.Host
			if host == "" && len(ingress.Status.LoadBalancer.Ingress) > 0 {
				// If the host is not specified in the rule, try to get it from the LoadBalancer status
				lbIngress := ingress.Status.LoadBalancer.Ingress[0]
				if lbIngress.Hostname != "" {
					host = lbIngress.Hostname
				} else if lbIngress.IP != "" {
					host = lbIngress.IP
				}
			}
			// Iterate through each path
			for _, path := range rule.HTTP.Paths {
				backend := path.Backend
				if backend.Service != nil &&
					backend.Service.Name == serviceName &&
					backend.Service.Port.Number == servicePort {
					scheme := "http"
					if hasTLSForHost(ingress, host) {
						scheme = "https"
					}
					pathStr := path.Path
					if pathStr == "" {
						pathStr = "/"
					}
					return scheme + "://" + host + pathStr
				}
			}
		}
	}
	return ""
}

// hasTLSForHost examine if the ingress has TLS configured for the given host
func hasTLSForHost(ingress networkingv1.Ingress, host string) bool {
	for _, tls := range ingress.Spec.TLS {
		for _, tlsHost := range tls.Hosts {
			if tlsHost == host {
				return true
			}
		}
	}
	return false
}

// formatCPUUsage formats CPU usage quantity to human-readable format
func formatCPUUsage(quantity resource.Quantity) string {
	milliValue := quantity.MilliValue()
	if milliValue >= 1000 {
		cores := float64(milliValue) / 1000
		if cores == float64(int(cores)) {
			return fmt.Sprintf("%d", int(cores))
		}
		return fmt.Sprintf("%.2f", cores)
	}
	// Display as milli-cores for < 1 core
	return fmt.Sprintf("%dm", milliValue)
}

// formatMemoryUsage formats memory usage quantity to human-readable format
func formatMemoryUsage(quantity resource.Quantity) string {
	bytes := quantity.Value()
	switch {
	case bytes >= 1<<30: // >= 1 GiB
		gb := float64(bytes) / (1 << 30)
		if gb >= 10 {
			return fmt.Sprintf("%.0fGi", gb)
		}
		return fmt.Sprintf("%.1fGi", gb)
	case bytes >= 1<<20: // >= 1 MiB
		mb := float64(bytes) / (1 << 20)
		if mb >= 10 {
			return fmt.Sprintf("%.0fMi", mb)
		}
		return fmt.Sprintf("%.1fMi", mb)
	case bytes >= 1<<10: // >= 1 KiB
		return fmt.Sprintf("%.0fKi", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d", bytes)
	}
}

// getNamespaceMetrics aggregates live pod usage for a namespace and expresses it
// as a percentage of what the namespace's deployments are allowed to use.
//
// The metrics API is an OPTIONAL aggregated API: a cluster without
// metrics-server serves no metrics.k8s.io at all. That is a missing capability,
// not a failure, so it returns (nil, nil) and the view simply carries no metrics
// block rather than failing the whole read.
func getNamespaceMetrics(ctx context.Context, c K8sClient, namespace string, deployments []appsv1.Deployment, lang string) (*object.ResourceMetrics, error) {
	podMetrics, err := listInto[metricsv1beta1.PodMetrics](ctx, c, podMetricsGVR, namespace)
	if err != nil {
		if errors.Is(err, ErrK8sNotFound) || strings.Contains(err.Error(), "metrics.k8s.io") {
			return nil, nil
		}
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to get pod metrics: %v"), err))
	}

	totalCPU := resource.NewQuantity(0, resource.DecimalSI)
	totalMemory := resource.NewQuantity(0, resource.BinarySI)
	// Aggregate resource usage from all pods
	for _, pod := range podMetrics {
		for _, container := range pod.Containers {
			if cpuUsage, ok := container.Usage[v1.ResourceCPU]; ok {
				totalCPU.Add(cpuUsage)
			}
			if memUsage, ok := container.Usage[v1.ResourceMemory]; ok {
				totalMemory.Add(memUsage)
			}
		}
	}

	cpuPercentage, memPercentage := usageAgainstLimits(*totalCPU, *totalMemory, deployments)
	return &object.ResourceMetrics{
		CPUUsage:         formatCPUUsage(*totalCPU),
		CPUPercentage:    cpuPercentage,
		MemoryUsage:      formatMemoryUsage(*totalMemory),
		MemoryPercentage: memPercentage,
		PodCount:         len(podMetrics),
	}, nil
}

// usageAgainstLimits expresses usage as a percentage of the namespace's declared
// limits (summed per replica). Pure. A workload that declares no limits has no
// denominator, so its percentage stays zero rather than becoming a made-up number.
func usageAgainstLimits(cpu, memory resource.Quantity, deployments []appsv1.Deployment) (cpuPct, memPct float64) {
	totalCPULimits := resource.NewQuantity(0, resource.DecimalSI)
	totalMemoryLimits := resource.NewQuantity(0, resource.BinarySI)
	for _, deploy := range deployments {
		replicas := int32(1)
		if deploy.Spec.Replicas != nil {
			replicas = *deploy.Spec.Replicas
		}
		for _, container := range deploy.Spec.Template.Spec.Containers {
			if container.Resources.Limits == nil {
				continue
			}
			if cpuLimit, ok := container.Resources.Limits[v1.ResourceCPU]; ok {
				for i := int32(0); i < replicas; i++ {
					totalCPULimits.Add(cpuLimit)
				}
			}
			if memLimit, ok := container.Resources.Limits[v1.ResourceMemory]; ok {
				for i := int32(0); i < replicas; i++ {
					totalMemoryLimits.Add(memLimit)
				}
			}
		}
	}
	if totalCPULimits.MilliValue() > 0 {
		cpuPct = float64(cpu.MilliValue()) / float64(totalCPULimits.MilliValue()) * 100
	}
	if totalMemoryLimits.Value() > 0 {
		memPct = float64(memory.Value()) / float64(totalMemoryLimits.Value()) * 100
	}
	return cpuPct, memPct
}
