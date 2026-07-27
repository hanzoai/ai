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

// The runtime shape of a deployed application, as served under Application.Details.
// Assembled from a cluster by github.com/hanzoai/ai/cluster.
type ApplicationView struct {
	Services    []ServiceDetail    `json:"services"`
	Credentials []EnvVariable      `json:"credentials"`
	Deployments []DeploymentDetail `json:"deployments"`
	Events      []ApplicationEvent `json:"events"`
	Status      string             `json:"status"`
	CreatedTime string             `json:"createdTime"`
	Namespace   string             `json:"namespace"`
	Metrics     *ResourceMetrics   `json:"metrics,omitempty"`
}

// ResourceMetrics represents resource usage metrics
type ResourceMetrics struct {
	CPUUsage         string  `json:"cpuUsage"`         // CPU usage (e.g., "120m" for 120 millicores)
	CPUPercentage    float64 `json:"cpuPercentage"`    // CPU usage percentage (0-100)
	MemoryUsage      string  `json:"memoryUsage"`      // Memory usage (e.g., "256Mi" for 256 mebibyte)
	MemoryPercentage float64 `json:"memoryPercentage"` // Memory usage percentage (0-100)
	PodCount         int     `json:"podCount"`         // Number of active pods
}
type ServiceDetail struct {
	Name         string        `json:"name"`
	Type         string        `json:"type"`
	ClusterIP    string        `json:"clusterIP"`
	ExternalIP   string        `json:"externalIP"`
	Ports        []ServicePort `json:"ports"`
	InternalHost string        `json:"internalHost"`
	ExternalHost string        `json:"externalHost"`
	CreatedTime  string        `json:"createdTime"`
}
type ServicePort struct {
	Name     string `json:"name"`
	Port     int32  `json:"port"`
	NodePort int32  `json:"nodePort,omitempty"`
	Protocol string `json:"protocol"`
	URL      string `json:"url,omitempty"`
}
type DeploymentDetail struct {
	Name          string            `json:"name"`
	Replicas      int32             `json:"replicas"`
	ReadyReplicas int32             `json:"readyReplicas"`
	Containers    []ContainerDetail `json:"containers"`
	CreatedTime   string            `json:"createdTime"`
	Status        string            `json:"status"`
}
type ContainerDetail struct {
	Name      string           `json:"name"`
	Image     string           `json:"image"`
	Resources ResourceRequests `json:"resources"`
}
type ResourceRequests struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type ApplicationEvent struct {
	Name           string `json:"name"`           // Event name
	Type           string `json:"type"`           // Event type: Normal, Warning
	Reason         string `json:"reason"`         // Event reason
	Message        string `json:"message"`        // Event message
	InvolvedObject string `json:"involvedObject"` // Related object
	Source         string `json:"source"`         // Event source
	Count          int    `json:"count"`          // Event occurrence count
	FirstTime      string `json:"firstTime"`      // First occurrence time
	LastTime       string `json:"lastTime"`       // Last occurrence time
}

