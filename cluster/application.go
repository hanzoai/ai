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
	"time"

	"github.com/hanzoai/ai/object"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/util"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
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
	c, err := ensure(lang)
	if err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to initialize k8s client: %v"), err))
	}
	ctx := context.TODO()
	// Create namespace if it doesn't exist
	if err := createNamespaceIfNotExists(ctx, c, application.Namespace); err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to create namespace: %v"), err))
	}
	// Deploy the manifest
	if err := applyManifest(ctx, c, application.Manifest, application.Namespace, lang); err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to deploy manifest: %v"), err))
	}
	err = setStatus(application.Owner, application.Name, object.StatusPending, lang)
	if err != nil {
		return false, err
	}
	return true, nil
}

func Undeploy(owner, name, namespace string, lang string) (bool, error) {
	c, err := ensure(lang)
	if err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to initialize k8s client: %v"), err))
	}
	// Delete the entire namespace
	err = c.Delete(context.TODO(), namespacesGVR.group, namespacesGVR.version, namespacesGVR.resource, "", namespace)
	if err != nil && !errors.Is(err, ErrK8sNotFound) {
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
	c, err := ensure(lang)
	if err != nil {
		return "", err
	}
	pods, err := listInto[v1.Pod](context.TODO(), c, podsGVR, namespace)
	if err != nil {
		if errors.Is(err, ErrK8sNotFound) {
			return "namespace or pods not found", nil
		}
		return "", fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to list pods in namespace %s: %w"), namespace, err))
	}
	if len(pods) == 0 {
		return "no pods were found in the application namespace to inspect", nil
	}
	reasons := analyzePodFailures(pods)
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
	c, err := ensure(lang)
	if err != nil {
		return object.StatusUnknown, err
	}
	ctx := context.TODO()
	var ns v1.Namespace
	if err := getInto(ctx, c, namespacesGVR, "", namespace, &ns); err != nil {
		if errors.Is(err, ErrK8sNotFound) {
			err = setStatus(owner, name, object.StatusNotDeployed, lang)
			if err != nil {
				return "", err
			}
			return object.StatusNotDeployed, nil
		}
		return object.StatusUnknown, err
	}
	if ns.Status.Phase == v1.NamespaceTerminating {
		pods, _ := listInto[v1.Pod](ctx, c, podsGVR, namespace)
		services, _ := listInto[v1.Service](ctx, c, servicesGVR, namespace)
		deployments, _ := listInto[appsv1.Deployment](ctx, c, deploymentsGVR, namespace)
		if len(pods) == 0 && len(services) == 0 && len(deployments) == 0 {
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
	deployments, err := listInto[appsv1.Deployment](ctx, c, deploymentsGVR, namespace)
	if err != nil {
		return object.StatusUnknown, err
	}
	statefulSets, err := listInto[appsv1.StatefulSet](ctx, c, statefulSetsGVR, namespace)
	if err != nil {
		return object.StatusUnknown, err
	}
	if len(deployments) == 0 && len(statefulSets) == 0 {
		err = setStatus(owner, name, object.StatusNotDeployed, lang)
		if err != nil {
			return "", err
		}
		return object.StatusNotDeployed, nil
	}
	// Check if all deployments are ready
	for _, deployment := range deployments {
		if deployment.Status.ReadyReplicas < deployment.Status.Replicas {
			err = setStatus(owner, name, object.StatusPending, lang)
			if err != nil {
				return "", err
			}
			return object.StatusPending, nil
		}
	}
	// Check if all statefulsets are ready
	for _, statefulSet := range statefulSets {
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
