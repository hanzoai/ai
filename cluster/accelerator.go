// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"sort"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
)

// accelerator.go answers ONE question for the two paths that submit a workload to a
// Kubernetes scheduler: what resource name does THIS cluster answer an accelerator
// requirement with?
//
// A job needs ACCELERATORS. `nvidia.com/gpu` is not that requirement — it is one
// vendor's name for it. Writing that name into a scheduler contract had two costs:
// a job became unroutable to an AMD or Apple machine that could have run it, and a
// cluster whose nodes advertise no accelerator at all still ACCEPTED the workload,
// because a resource name a scheduler has never heard of is not a syntax error. It
// is a pod that pends forever.
//
// So the requirement stays a COUNT, and the NAME is discovered: intersect what this
// cluster's nodes actually advertise as allocatable with the names accelerators are
// advertised under. The emitted name is therefore always one some node really
// provides, and when the intersection is empty the cluster cannot satisfy the
// requirement at all — which is said out loud, at submit time, instead of being
// deferred to a workload that never schedules.
//
// The same requirement vocabulary a linked machine advertises in cloud/apps/agents
// (Spec.GPUs{Vendor,Model,Memory}, and Need with no vendor field at all) is the
// vocabulary here: accelerators, a count, and "unknown never clears a floor".
// Matching a Need against a machine's advertised Spec is agents.Spec.Satisfies;
// matching the same requirement against a cluster's advertised node resources is
// selectAccelerator below. Two substrates — a BYO box that claims work by polling,
// and a cluster that schedules what it is given — one vocabulary, and neither one
// asks a caller to name a vendor.

// deviceResources are the extended-resource names accelerator device plugins
// advertise on a node.
//
// This table is RECOGNITION ONLY. It answers "is this advertised resource an
// accelerator?" — never "which one should a job ask for?". That second question is
// answered by the cluster, by advertising it. So adding a vendor here can never
// send a workload somewhere no node advertises, and removing one can only make this
// code fail to notice hardware that is really present. It is the one place a vendor
// name appears in a scheduling contract in this repo.
var deviceResources = map[string]bool{
	"nvidia.com/gpu":        true,
	"amd.com/gpu":           true,
	"gpu.intel.com/i915":    true,
	"habana.ai/gaudi":       true,
	"google.com/tpu":        true,
	"aws.amazon.com/neuron": true,
}

// selectAccelerator picks the resource name to ask a scheduler for, given what the
// cluster's nodes advertise (resource name → how many nodes advertise a non-zero
// amount of it). Pure, so a test can state a fleet as data.
//
// Ties are broken toward the accelerator the MOST nodes advertise, then by name, so
// a cluster with one leftover node of a second kind does not send every job to it,
// and the answer is deterministic. Reports false when nothing advertised is an
// accelerator — the caller must refuse, not guess a name.
func selectAccelerator(advertised map[string]int) (string, bool) {
	type candidate struct {
		name  string
		nodes int
	}
	var candidates []candidate
	for name, nodes := range advertised {
		if nodes > 0 && deviceResources[name] {
			candidates = append(candidates, candidate{name: name, nodes: nodes})
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].nodes != candidates[j].nodes {
			return candidates[i].nodes > candidates[j].nodes
		}
		return candidates[i].name < candidates[j].name
	})
	return candidates[0].name, true
}

// acceleratorLimits renders the resource-limits map that asks for n accelerators of
// the given resource. Pure. A count below one is a bug in the caller's arithmetic,
// not a request for zero accelerators, so it floors at one.
func acceleratorLimits(resource string, n int) map[string]interface{} {
	if n < 1 {
		n = 1
	}
	return map[string]interface{}{resource: strconv.Itoa(n)}
}

// advertisedAccelerators reads what this cluster's nodes advertise as allocatable,
// counting the nodes offering a non-zero amount of each resource. A read failure
// is returned rather than swallowed: an unreachable cluster must never be
// mistaken for a cluster without accelerators, because that mistake refuses a
// job the hardware could have run.
func advertisedAccelerators(ctx context.Context, c K8sClient) (map[string]int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	nodes, err := listInto[v1.Node](ctx, c, nodesGVR, "")
	if err != nil {
		return nil, fmt.Errorf("failed to read the cluster's nodes to find an accelerator: %w", err)
	}
	advertised := make(map[string]int)
	for _, node := range nodes {
		for name, quantity := range node.Status.Allocatable {
			if quantity.IsZero() {
				continue
			}
			advertised[string(name)]++
		}
	}
	return advertised, nil
}

// acceleratorRequest is the ONE call a workload renderer makes to turn "I need n
// accelerators" into a scheduler contract this cluster can actually satisfy. It
// refuses — loudly, before anything is created — when no node advertises one.
func acceleratorRequest(ctx context.Context, c K8sClient, n int) (map[string]interface{}, error) {
	advertised, err := advertisedAccelerators(ctx, c)
	if err != nil {
		return nil, err
	}
	resource, ok := selectAccelerator(advertised)
	if !ok {
		return nil, errNoAccelerators(n)
	}
	return acceleratorLimits(resource, n), nil
}

// errNoAccelerators is the refusal, stated once: what was asked for, what the
// cluster can do about it, and what an operator must change. This is the loud
// failure that a baked-in resource name was hiding — a scheduler will accept a
// resource name it has never heard of and simply never schedule the pod.
func errNoAccelerators(n int) error {
	if n < 1 {
		n = 1
	}
	return fmt.Errorf("this cluster advertises no accelerators, so a workload requiring %d cannot be scheduled on it: no node offers an accelerator resource as allocatable. Add a GPU node pool, or install the device plugin that advertises the accelerators the nodes already have", n)
}
