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
	"github.com/hanzoai/ai/object"

	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/i18n"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
)

var (
	metricsClient *metricsclientset.Clientset
	metricsOnce   sync.Once
)

// initMetricsClient init metrics client
func initMetricsClient(lang string) error {
	if client == nil || client.config == nil {
		return fmt.Errorf("%s", i18n.Translate(lang, "object:k8s client not initialized"))
	}
	var err error
	metricsOnce.Do(func() {
		metricsClient, err = metricsclientset.NewForConfig(client.config)
	})
	return err
}

// getNamespaceMetrics retrieves namespace metrics from cache with API fallback
func getNamespaceMetrics(namespace string, lang string) (*object.ResourceMetrics, error) {
	if mirror != nil && mirror.started {
		if cachedMetrics, found := mirror.getNamespaceMetricsFromCache(namespace); found {
			if time.Since(cachedMetrics.LastUpdated) < 5*time.Minute {
				return &object.ResourceMetrics{
					CPUUsage:         formatCPUUsage(cachedMetrics.TotalCPU),
					CPUPercentage:    cachedMetrics.CPUPercentage,
					MemoryUsage:      formatMemoryUsage(cachedMetrics.TotalMemory),
					MemoryPercentage: cachedMetrics.MemoryPercentage,
					PodCount:         cachedMetrics.PodCount,
				}, nil
			}
		}
	}
	if err := initMetricsClient(lang); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()
	var metrics *cachedMetrics
	var err error
	if mirror != nil && mirror.started {
		metrics, err = calculateNamespaceMetrics(ctx, metricsClient, namespace, mirror.deployCache, &mirror.mu, lang)
	} else {
		metrics, err = calculateNamespaceMetrics(ctx, metricsClient, namespace, nil, nil, lang)
	}
	if err != nil {
		if errors.IsNotFound(err) || strings.Contains(err.Error(), "metrics.k8s.io") {
			return nil, nil
		}
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to get pod metrics: %v"), err))
	}
	if metrics == nil {
		return nil, nil
	}
	return &object.ResourceMetrics{
		CPUUsage:         formatCPUUsage(metrics.TotalCPU),
		CPUPercentage:    metrics.CPUPercentage,
		MemoryUsage:      formatMemoryUsage(metrics.TotalMemory),
		MemoryPercentage: metrics.MemoryPercentage,
		PodCount:         metrics.PodCount,
	}, nil
}

// getExternalHost attempts to get k8s server IP first, then falls back to provided host
func getExternalHost(fallbackHost string) string {
	if cachedHost != "" {
		return cachedHost
	}
	return fallbackHost
}

// parseHost extracts server host from kubeconfig content
func parseHost(configText string, lang string) (string, error) {
	if strings.TrimSpace(configText) == "" {
		return "", fmt.Errorf("%s", i18n.Translate(lang, "object:kubeconfig content is empty"))
	}
	config, err := clientcmd.RESTConfigFromKubeConfig([]byte(configText))
	if err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to parse kubeconfig: %v"), err))
	}
	if config.Host == "" {
		return "", fmt.Errorf("%s", i18n.Translate(lang, "object:server address not found"))
	}
	serverURL, err := url.Parse(config.Host)
	if err != nil {
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to parse server URL: %v"), err))
	}
	host := serverURL.Hostname()
	if host == "" {
		return "", fmt.Errorf("%s", i18n.Translate(lang, "object:unable to extract host"))
	}
	return host, nil
}

// View retrieves application view from cache with fallback
func View(namespace string, lang string) (*object.ApplicationView, error) {
	if err := ensure(lang); err != nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to initialize k8s client: %v"), err))
	}
	if !client.connected {
		return nil, fmt.Errorf("%s", i18n.Translate(lang, "object:k8s client not connected"))
	}
	// Try to get namespace from cache first
	var ns *v1.Namespace
	var nsFound bool
	if mirror != nil && mirror.started {
		ns = mirror.getNamespace(namespace)
		nsFound = (ns != nil)
	}
	// Fallback to API call with timeout if not in cache
	if !nsFound {
		ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
		defer cancel()
		apiNs, err := client.clientSet.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return &object.ApplicationView{
					Services:    []object.ServiceDetail{},
					Credentials: []object.EnvVariable{},
					Deployments: []object.DeploymentDetail{},
					Events:      []object.ApplicationEvent{},
					Status:      object.StatusNotDeployed,
					Namespace:   namespace,
				}, nil
			}
			return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to get namespace: %v"), err))
		}
		ns = apiNs
	}
	details := &object.ApplicationView{
		Services:    []object.ServiceDetail{},
		Credentials: []object.EnvVariable{},
		Deployments: []object.DeploymentDetail{},
		Events:      []object.ApplicationEvent{},
		Status:      object.StatusRunning,
		CreatedTime: ns.CreationTimestamp.Format("2006-01-02 15:04:05"),
		Namespace:   namespace,
	}
	// Get data from cache with fallback to API
	nodeIPs := getNodeIPsFromCache()
	details.Services = getServicesFromCache(namespace, nodeIPs)
	details.Deployments = getDeploymentsFromCache(namespace)
	details.Credentials = getCredentialsFromCache(namespace)
	details.Events = getEventsFromCache(namespace) // Added event retrieval
	if metrics, err := getNamespaceMetrics(namespace, lang); err == nil && metrics != nil {
		details.Metrics = metrics
	}
	return details, nil
}

// getNodeIPsFromCache retrieves node IPs from cache or fallback to API
func getNodeIPsFromCache() []string {
	var nodes []*v1.Node
	// Try cache first
	if mirror != nil && mirror.started {
		nodes = mirror.getNodes()
	}
	var nodeIPs []string
	for _, node := range nodes {
		// Try external IP first
		for _, addr := range node.Status.Addresses {
			if addr.Type == v1.NodeExternalIP && addr.Address != "" {
				nodeIPs = append(nodeIPs, addr.Address)
				break
			}
		}
		// Fallback to internal IP if no external IP found
		if len(nodeIPs) == 0 {
			for _, addr := range node.Status.Addresses {
				if addr.Type == v1.NodeInternalIP && addr.Address != "" {
					nodeIPs = append(nodeIPs, addr.Address)
					break
				}
			}
		}
	}
	return nodeIPs
}

// getServicesFromCache retrieves services from cache or fallback to API
func getServicesFromCache(namespace string, nodeIPs []string) []object.ServiceDetail {
	var services []*v1.Service
	// Try cache first
	if mirror != nil && mirror.started {
		services = mirror.getServices(namespace)
	}
	ingresses := getIngressFromCache(namespace)
	var serviceDetails []object.ServiceDetail
	for _, svc := range services {
		detail := object.ServiceDetail{
			Name:         svc.Name,
			Type:         string(svc.Spec.Type),
			ClusterIP:    svc.Spec.ClusterIP,
			Ports:        []object.ServicePort{},
			CreatedTime:  svc.CreationTimestamp.Format("2006-01-02 15:04:05"),
			InternalHost: fmt.Sprintf("%s.%s.svc.cluster.local", svc.Name, namespace),
		}
		// Determine external access based on service type
		var host string
		switch svc.Spec.Type {
		case v1.ServiceTypeLoadBalancer:
			if len(svc.Status.LoadBalancer.Ingress) > 0 {
				ingress := svc.Status.LoadBalancer.Ingress[0]
				if ingress.IP != "" {
					detail.ExternalIP = ingress.IP
					host = ingress.IP
				} else if ingress.Hostname != "" {
					host = ingress.Hostname
				}
			}
		case v1.ServiceTypeNodePort:
			if len(nodeIPs) > 0 {
				host = nodeIPs[0]
			}
		case v1.ServiceTypeClusterIP:
			host = getExternalHost("")
		}
		detail.ExternalHost = getExternalHost(host)
		for _, port := range svc.Spec.Ports {
			servicePort := object.ServicePort{
				Name:     port.Name,
				Port:     port.Port,
				Protocol: string(port.Protocol),
			}
			// get URL from Ingress
			ingressURL := findIngressURL(svc.Name, port.Port, ingresses)
			if ingressURL != "" {
				servicePort.URL = ingressURL
			} else if port.NodePort != 0 {
				servicePort.NodePort = port.NodePort
				if detail.ExternalHost != "" {
					servicePort.URL = fmt.Sprintf("%s:%d", detail.ExternalHost, port.NodePort)
				}
			}
			detail.Ports = append(detail.Ports, servicePort)
		}
		serviceDetails = append(serviceDetails, detail)
	}
	return serviceDetails
}

func getIngressFromCache(namespace string) []*networkingv1.Ingress {
	var ingresses []*networkingv1.Ingress
	// First, try to get from the cache
	if mirror != nil && mirror.started {
		ingresses = mirror.getIngresses(namespace)
	}
	// If the cache is empty, try to fetch from the API
	if len(ingresses) == 0 {
		ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
		defer cancel()
		ingressList, err := client.clientSet.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
		if err == nil {
			for i := range ingressList.Items {
				ingresses = append(ingresses, &ingressList.Items[i])
			}
		}
	}
	return ingresses
}

// getDeploymentsFromCache retrieves deployments from cache or fallback to API
func getDeploymentsFromCache(namespace string) []object.DeploymentDetail {
	var deployments []*appsv1.Deployment
	// Try cache first
	if mirror != nil && mirror.started {
		deployments = mirror.getDeployments(namespace)
	}
	var deploymentDetails []object.DeploymentDetail
	for _, deployment := range deployments {
		detail := object.DeploymentDetail{
			Name:          deployment.Name,
			Replicas:      *deployment.Spec.Replicas,
			ReadyReplicas: deployment.Status.ReadyReplicas,
			Containers:    []object.ContainerDetail{},
			CreatedTime:   deployment.CreationTimestamp.Format("2006-01-02 15:04:05"),
		}
		// Determine deployment status
		if detail.ReadyReplicas == detail.Replicas {
			detail.Status = "Running"
		} else if detail.ReadyReplicas > 0 {
			detail.Status = "Partially Ready"
		} else {
			detail.Status = "Not Ready"
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			containerDetail := object.ContainerDetail{
				Name:  container.Name,
				Image: container.Image,
			}
			if container.Resources.Requests != nil {
				if cpuRequest := container.Resources.Requests[v1.ResourceCPU]; !cpuRequest.IsZero() {
					containerDetail.Resources.CPU = cpuRequest.String()
				}
				if memoryRequest := container.Resources.Requests[v1.ResourceMemory]; !memoryRequest.IsZero() {
					containerDetail.Resources.Memory = memoryRequest.String()
				}
			}
			detail.Containers = append(detail.Containers, containerDetail)
		}
		deploymentDetails = append(deploymentDetails, detail)
	}
	return deploymentDetails
}

// getCredentialsFromCache extracts environment variables containing sensitive information
func getCredentialsFromCache(namespace string) []object.EnvVariable {
	var deployments []*appsv1.Deployment
	// Try cache first
	if mirror != nil && mirror.started {
		deployments = mirror.getDeployments(namespace)
	}
	credentialKeywords := []string{
		"PASSWORD", "PASS", "SECRET", "KEY", "TOKEN", "AUTH",
		"USER", "USERNAME", "LOGIN", "CREDENTIAL", "DATABASE_URL",
		"DB_PASSWORD", "DB_USER", "ADMIN_PASSWORD", "ROOT_PASSWORD",
	}
	var credentials []object.EnvVariable
	for _, deployment := range deployments {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				envNameUpper := strings.ToUpper(env.Name)
				isCredential := false
				for _, keyword := range credentialKeywords {
					if strings.Contains(envNameUpper, keyword) {
						isCredential = true
						break
					}
				}
				if isCredential {
					value := env.Value
					if env.ValueFrom != nil {
						if env.ValueFrom.SecretKeyRef != nil {
							value = fmt.Sprintf("Secret: %s.%s", env.ValueFrom.SecretKeyRef.Name, env.ValueFrom.SecretKeyRef.Key)
						} else if env.ValueFrom.ConfigMapKeyRef != nil {
							value = fmt.Sprintf("ConfigMap: %s.%s", env.ValueFrom.ConfigMapKeyRef.Name, env.ValueFrom.ConfigMapKeyRef.Key)
						}
					}
					credentials = append(credentials, object.EnvVariable{
						Name:  env.Name,
						Value: value,
					})
				}
			}
		}
	}
	return credentials
}

// getEventsFromCache retrieves namespace-related events from cache or API
func getEventsFromCache(namespace string) []object.ApplicationEvent {
	var events []*v1.Event
	// Try cache first
	if mirror != nil && mirror.started {
		events = mirror.getEvents(namespace)
	}
	// If cache is empty, get from API
	if len(events) == 0 {
		ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
		defer cancel()
		eventList, err := client.clientSet.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
		if err == nil {
			for i := range eventList.Items {
				events = append(events, &eventList.Items[i])
			}
		}
	}
	return convertEventsToApplicationEvents(events)
}

// convertEventsToDetails converts Kubernetes Events to EventDetail
func convertEventsToApplicationEvents(events []*v1.Event) []object.ApplicationEvent {
	eventDetails := make([]object.ApplicationEvent, 0)
	for _, event := range events {
		// Format involved object information
		involvedObj := fmt.Sprintf("%s/%s",
			strings.ToLower(event.InvolvedObject.Kind),
			event.InvolvedObject.Name)
		// Format event source information
		source := event.Source.Component
		if event.Source.Host != "" {
			source = fmt.Sprintf("%s@%s", source, event.Source.Host)
		}
		detail := object.ApplicationEvent{
			Name:           event.Name,
			Type:           event.Type,
			Reason:         event.Reason,
			Message:        event.Message,
			InvolvedObject: involvedObj,
			Source:         source,
			Count:          int(event.Count), // Convert int32 to int
			FirstTime:      event.FirstTimestamp.Format("2006-01-02 15:04:05"),
			LastTime:       event.LastTimestamp.Format("2006-01-02 15:04:05"),
		}
		eventDetails = append(eventDetails, detail)
	}
	sort.Slice(eventDetails, func(i, j int) bool {
		return eventDetails[i].LastTime > eventDetails[j].LastTime
	})
	if len(eventDetails) > 50 {
		eventDetails = eventDetails[:50]
	}
	return eventDetails
}
