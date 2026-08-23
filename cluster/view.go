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
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/ai/object"

	"github.com/hanzoai/ai/i18n"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// readTimeout bounds one cluster read. The view is assembled for a UI request,
// so a wedged API server must surface as a partial view rather than a hung
// handler.
const readTimeout = 5 * time.Second

// getExternalHost prefers the cluster API server's host — the address a caller
// outside the cluster can actually reach — and falls back to the per-service
// host when it is unknown.
func getExternalHost(c K8sClient, fallbackHost string) string {
	if host := c.Host(); host != "" {
		return host
	}
	return fallbackHost
}

// View assembles the live picture of one application's namespace.
func View(namespace string, lang string) (*object.ApplicationView, error) {
	c, err := ensure(lang)
	if err != nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to initialize k8s client: %v"), err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	var ns v1.Namespace
	if err := getInto(ctx, c, namespacesGVR, "", namespace, &ns); err != nil {
		if errors.Is(err, ErrK8sNotFound) {
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

	details := &object.ApplicationView{
		Services:    []object.ServiceDetail{},
		Credentials: []object.EnvVariable{},
		Deployments: []object.DeploymentDetail{},
		Events:      []object.ApplicationEvent{},
		Status:      object.StatusRunning,
		CreatedTime: ns.CreationTimestamp.Format("2006-01-02 15:04:05"),
		Namespace:   namespace,
	}

	// One read of the namespace's workloads serves both the deployment view and
	// the credential projection, so the view is internally consistent.
	deployments, _ := listInto[appsv1.Deployment](ctx, c, deploymentsGVR, namespace)

	details.Services = getServices(ctx, c, namespace, getNodeIPs(ctx, c))
	details.Deployments = describeDeployments(deployments)
	details.Credentials = describeCredentials(deployments)
	details.Events = getEvents(ctx, c, namespace)
	if metrics, err := getNamespaceMetrics(ctx, c, namespace, deployments, lang); err == nil && metrics != nil {
		details.Metrics = metrics
	}
	return details, nil
}

// getNodeIPs returns the externally reachable node addresses, preferring
// external IPs and falling back to internal ones.
func getNodeIPs(ctx context.Context, c K8sClient) []string {
	nodes, err := listInto[v1.Node](ctx, c, nodesGVR, "")
	if err != nil {
		return nil
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

// getServices projects the namespace's services, resolving each port's
// externally reachable URL through an Ingress rule when one covers it.
func getServices(ctx context.Context, c K8sClient, namespace string, nodeIPs []string) []object.ServiceDetail {
	services, err := listInto[v1.Service](ctx, c, servicesGVR, namespace)
	if err != nil {
		return nil
	}
	ingresses, _ := listInto[networkingv1.Ingress](ctx, c, ingressesGVR, namespace)

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
			host = getExternalHost(c, "")
		}
		detail.ExternalHost = getExternalHost(c, host)
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

// describeDeployments projects deployments into the view shape. Pure.
func describeDeployments(deployments []appsv1.Deployment) []object.DeploymentDetail {
	var deploymentDetails []object.DeploymentDetail
	for _, deployment := range deployments {
		detail := object.DeploymentDetail{
			Name:          deployment.Name,
			ReadyReplicas: deployment.Status.ReadyReplicas,
			Containers:    []object.ContainerDetail{},
			CreatedTime:   deployment.CreationTimestamp.Format("2006-01-02 15:04:05"),
		}
		if deployment.Spec.Replicas != nil {
			detail.Replicas = *deployment.Spec.Replicas
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

// credentialKeywords name the env vars the view surfaces as credentials. A
// value sourced from a Secret or ConfigMap is reported as its REFERENCE, never
// resolved — the view names where a credential comes from, it never reads one.
var credentialKeywords = []string{
	"PASSWORD", "PASS", "SECRET", "KEY", "TOKEN", "AUTH",
	"USER", "USERNAME", "LOGIN", "CREDENTIAL", "DATABASE_URL",
	"DB_PASSWORD", "DB_USER", "ADMIN_PASSWORD", "ROOT_PASSWORD",
}

// describeCredentials extracts environment variables containing sensitive
// information. Pure.
func describeCredentials(deployments []appsv1.Deployment) []object.EnvVariable {
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
					// What a view of credentials is FOR is knowing which ones a
					// deployment expects and where each comes from — never what any of
					// them is. A variable set inline carries its value in the pod spec,
					// so reading env.Value here put the credential itself in the answer;
					// the two ValueFrom branches were already saying the useful thing.
					value := "set on the deployment"
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

// getEvents retrieves namespace-related events.
func getEvents(ctx context.Context, c K8sClient, namespace string) []object.ApplicationEvent {
	events, err := listInto[v1.Event](ctx, c, eventsGVR, namespace)
	if err != nil {
		return []object.ApplicationEvent{}
	}
	return convertEventsToApplicationEvents(events)
}

// convertEventsToApplicationEvents converts Kubernetes Events to
// ApplicationEvent, newest first, capped at 50. Pure.
func convertEventsToApplicationEvents(events []v1.Event) []object.ApplicationEvent {
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
