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
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/ai/object"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func setStatus(owner string, name string, status string, lang string) error {
	application, err := object.GetApplication(util.GetIdFromOwnerAndName(owner, name))
	if err != nil {
		return err
	}
	if application == nil {
		return err
	}
	application.Status = status
	application.UpdatedTime = util.GetCurrentTime()
	_, err = object.UpdateApplication(util.GetIdFromOwnerAndName(owner, name), application)
	if err != nil {
		return err
	}
	return nil
}

func Deploy(application *object.Application, lang string) (bool, error) {
	if err := ensure(lang); err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to initialize k8s client: %v"), err))
	}
	if !client.connected {
		return false, fmt.Errorf("%s", i18n.Translate(lang, "object:k8s client not connected to cluster"))
	}
	// Create namespace if it doesn't exist
	err := client.createNamespaceIfNotExists(application.Namespace)
	if err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to create namespace: %v"), err))
	}
	// Deploy the manifest
	err = deployManifest(application.Manifest, application.Namespace, lang)
	if err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to deploy manifest: %v"), err))
	}
	err = setStatus(application.Owner, application.Name, object.StatusPending, lang)
	if err != nil {
		return false, err
	}
	return true, nil
}

func Undeploy(owner, name, namespace string, lang string) (bool, error) {
	if err := ensure(lang); err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to initialize k8s client: %v"), err))
	}
	if !client.connected {
		return false, fmt.Errorf("%s", i18n.Translate(lang, "object:k8s client not connected to cluster"))
	}
	// Delete the entire namespace
	err := client.clientSet.CoreV1().Namespaces().Delete(
		context.TODO(),
		namespace,
		metav1.DeleteOptions{},
	)
	if err != nil && !errors.IsNotFound(err) {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to delete namespace: %v"), err))
	}
	err = setStatus(owner, name, object.StatusTerminating, lang)
	if err != nil {
		return false, err
	}
	return true, nil
}

func DeploySync(application *object.Application, lang string) (bool, error) {
	// First deploy the application
	success, err := Deploy(application, lang)
	if err != nil {
		return false, err
	}
	if !success {
		return false, fmt.Errorf("%s", i18n.Translate(lang, "object:failed to deploy application"))
	}
	// Wait for deployment to be ready (with timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			err = setStatus(application.Owner, application.Name, object.StatusFailed, lang)
			if err != nil {
				return false, err
			}
			reason, err := FailureReason(application.Namespace, lang)
			if err != nil {
				return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:deployment failed, and could not retrieve failure details: %v"), err))
			}
			return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:deployment failed: %s"), reason))
		case <-ticker.C:
			status, err := Phase(application.Owner, application.Name, application.Namespace, lang)
			if err != nil {
				continue
			}
			switch status {
			case object.StatusRunning:
				if url, err := URL(application.Namespace, lang); err == nil && url != "" {
					application.URL = url
					application.Status = object.StatusRunning
					_, err := object.UpdateApplication(util.GetIdFromOwnerAndName(application.Owner, application.Name), application)
					if err != nil {
						return false, err
					}
				}
				return true, nil
			case object.StatusNotDeployed:
				return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:namespace %s is terminating and all resources have been cleaned up"), application.Namespace))
			default:
				continue
			}
		}
	}
}

// UndeploySync undeploys application and waits for it to be completely removed
func UndeploySync(owner, name, namespace string, lang string) (bool, error) {
	// First undeploy the application
	success, err := Undeploy(owner, name, namespace, lang)
	if err != nil {
		return false, err
	}
	if !success {
		return false, fmt.Errorf("%s", i18n.Translate(lang, "object:failed to start undeployment"))
	}
	// Wait for undeployment to complete (with timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("%s", i18n.Translate(lang, "object:undeployment timeout: application did not undeploy within 10 minutes"))
		case <-ticker.C:
			status, err := Phase(owner, name, namespace, lang)
			if err != nil {
				continue
			}
			switch status {
			case object.StatusNotDeployed:
				return true, nil
			case object.StatusTerminating:
				continue
			default:
				continue
			}
		}
	}
}

// FailureReason returns the failure reason for an application deployment
func FailureReason(namespace string, lang string) (string, error) {
	if namespace == "" {
		return "", fmt.Errorf("%s", i18n.Translate(lang, "object:namespace cannot be empty"))
	}
	if err := ensure(lang); err != nil {
		return "", err
	}
	if !client.connected {
		return "", fmt.Errorf("%s", i18n.Translate(lang, "object:k8s client is not connected to the cluster"))
	}
	pods, err := client.clientSet.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return "namespace or pods not found", nil
		}
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to list pods in namespace %s: %w"), namespace, err))
	}
	if len(pods.Items) == 0 {
		return "no pods were found in the application namespace to inspect", nil
	}
	reasons := analyzePodFailures(pods.Items)
	if len(reasons) > 0 {
		return strings.Join(reasons, "; "), nil
	}
	return "deployment failed for an unknown reason. Check pod logs and events in the namespace for more details.", nil
}

// analyzePodFailures analyzes pod failures and returns a list of failure reasons
func analyzePodFailures(pods []v1.Pod) []string {
	var reasons []string
	for _, pod := range pods {
		// Check if pod itself has failed
		if pod.Status.Phase == v1.PodFailed {
			reason := fmt.Sprintf("pod [%s] has failed", pod.Name)
			if pod.Status.Reason != "" {
				reason += fmt.Sprintf(" with reason: '%s'", pod.Status.Reason)
			}
			if pod.Status.Message != "" {
				reason += fmt.Sprintf(" and message: '%s'", pod.Status.Message)
			}
			reasons = append(reasons, reason)
			continue
		}
		// Check init containers
		for _, status := range pod.Status.InitContainerStatuses {
			if containerReason := analyzeContainerStatus(pod.Name, status.Name, "init container", status.State); containerReason != "" {
				reasons = append(reasons, containerReason)
			}
		}
		// Check main containers
		for _, status := range pod.Status.ContainerStatuses {
			if containerReason := analyzeContainerStatus(pod.Name, status.Name, "container", status.State); containerReason != "" {
				reasons = append(reasons, containerReason)
			}
		}
	}
	return reasons
}

// analyzeContainerStatus analyzes a single container's status and returns failure reason if any
func analyzeContainerStatus(podName, containerName, containerType string, state v1.ContainerState) string {
	if state.Waiting != nil && state.Waiting.Reason != "" {
		return fmt.Sprintf("pod [%s] %s [%s] is waiting: %s (%s)",
			podName, containerType, containerName, state.Waiting.Reason, state.Waiting.Message)
	}
	if state.Terminated != nil && state.Terminated.Reason != "" && state.Terminated.Reason != "Completed" {
		return fmt.Sprintf("pod [%s] %s [%s] terminated with exit code %d: %s (%s)",
			podName, containerType, containerName, state.Terminated.ExitCode,
			state.Terminated.Reason, state.Terminated.Message)
	}
	return ""
}

// Phase returns application status as string
func Phase(owner, name, namespace string, lang string) (string, error) {
	if err := ensure(lang); err != nil {
		return object.StatusUnknown, err
	}
	if !client.connected {
		return object.StatusUnknown, nil
	}
	ns, err := client.clientSet.CoreV1().Namespaces().Get(
		context.TODO(),
		namespace,
		metav1.GetOptions{},
	)
	if err != nil {
		if errors.IsNotFound(err) {
			err = setStatus(owner, name, object.StatusNotDeployed, lang)
			if err != nil {
				return "", err
			}
			return object.StatusNotDeployed, nil
		}
		return object.StatusUnknown, err
	}
	if ns.Status.Phase == v1.NamespaceTerminating {
		pods, _ := client.clientSet.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
		services, _ := client.clientSet.CoreV1().Services(namespace).List(context.TODO(), metav1.ListOptions{})
		deployments, _ := client.clientSet.AppsV1().Deployments(namespace).List(context.TODO(), metav1.ListOptions{})
		if len(pods.Items) == 0 && len(services.Items) == 0 && len(deployments.Items) == 0 {
			err = setStatus(owner, name, object.StatusNotDeployed, lang)
			if err != nil {
				return "", err
			}
			return object.StatusNotDeployed, nil
		}
		err = setStatus(owner, name, object.StatusTerminating, lang)
		if err != nil {
			return "", err
		}
		return object.StatusTerminating, nil
	}
	deployments, err := client.clientSet.AppsV1().Deployments(namespace).List(
		context.TODO(),
		metav1.ListOptions{},
	)
	if err != nil {
		return object.StatusUnknown, err
	}
	statefulSets, err := client.clientSet.AppsV1().StatefulSets(namespace).List(
		context.TODO(),
		metav1.ListOptions{},
	)
	if err != nil {
		return object.StatusUnknown, err
	}
	if len(deployments.Items) == 0 && len(statefulSets.Items) == 0 {
		err = setStatus(owner, name, object.StatusNotDeployed, lang)
		if err != nil {
			return "", err
		}
		return object.StatusNotDeployed, nil
	}
	// Check if all deployments are ready
	for _, deployment := range deployments.Items {
		if deployment.Status.ReadyReplicas < deployment.Status.Replicas {
			err = setStatus(owner, name, object.StatusPending, lang)
			if err != nil {
				return "", err
			}
			return object.StatusPending, nil
		}
	}
	// Check if all statefulsets are ready
	for _, statefulSet := range statefulSets.Items {
		if statefulSet.Status.ReadyReplicas < statefulSet.Status.Replicas {
			err = setStatus(owner, name, object.StatusPending, lang)
			if err != nil {
				return "", err
			}
			return object.StatusPending, nil
		}
	}
	err = setStatus(owner, name, object.StatusRunning, lang)
	if err != nil {
		return "", err
	}
	return object.StatusRunning, nil
}

// Helper function to deploy manifest (refactored from existing code)
func deployManifest(manifest, namespace string, lang string) error {
	// Split manifest by "---" separator
	docs := strings.Split(manifest, "---")
	for _, doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		err := client.deployResource(doc, namespace, lang)
		if err != nil {
			return fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to deploy resource: %v"), err))
		}
	}
	return nil
}
