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
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// hanzoK8s is what do-sfo3-hanzo-k8s advertises, measured off the live cluster by
// the same aggregation advertisedAccelerators performs: every node offers cpu,
// ephemeral-storage, memory and pods, and nothing else. It advertises no accelerator
// of any vendor, which is why a workload naming one could never be scheduled on it.
//
// hugepages-1Gi and hugepages-2Mi appear among the allocatable KEYS but at quantity
// zero on every node, so they are absent here — a resource advertised as zero is not
// capacity, and the aggregation drops it before anything can count it as hardware.
// Holding the cluster here as data means the day a GPU pool is added, this fixture is
// what changes.
var hanzoK8s = map[string]int{
	"cpu":               23,
	"ephemeral-storage": 23,
	"memory":            23,
	"pods":              23,
}

// THE bug, as a test. A cluster that advertises no accelerator cannot satisfy an
// accelerator requirement, and must say so — not accept the workload because
// `nvidia.com/gpu` happens to be a syntactically valid resource name.
func TestSelectAccelerator_AClusterWithNoneSatisfiesNothing(t *testing.T) {
	if resource, ok := selectAccelerator(hanzoK8s); ok {
		t.Fatalf("a cluster advertising no accelerator must not yield one; got %q", resource)
	}
}

// The answer follows the CLUSTER, never a constant. Swapping which vendor's plugin
// is installed swaps the answer, and no vendor is reachable unless a node advertises
// it — which is the whole difference between a requirement and a hardcode.
func TestSelectAccelerator_AnswerFollowsTheClusterNotAConstant(t *testing.T) {
	for _, want := range []string{
		"nvidia.com/gpu",
		"amd.com/gpu",
		"gpu.intel.com/i915",
		"habana.ai/gaudi",
		"google.com/tpu",
		"aws.amazon.com/neuron",
	} {
		advertised := map[string]int{"cpu": 4, "memory": 4, want: 4}
		got, ok := selectAccelerator(advertised)
		if !ok {
			t.Errorf("%s: a cluster advertising it must satisfy an accelerator requirement", want)
			continue
		}
		if got != want {
			t.Errorf("a cluster advertising only %s was answered with %s — a vendor is baked in somewhere", want, got)
		}
	}
}

// A node that advertises the resource but zero of it (drained, or the device plugin
// is down) is not capacity. advertisedAccelerators only counts non-zero quantities,
// and the selector refuses a zero count, so neither layer can invent hardware.
func TestSelectAccelerator_ZeroIsNotCapacity(t *testing.T) {
	if resource, ok := selectAccelerator(map[string]int{"nvidia.com/gpu": 0}); ok {
		t.Fatalf("zero nodes advertising an accelerator is not capacity; got %q", resource)
	}
}

// A cluster with one leftover node of a second kind must not receive every job.
// Ties break by name so the answer is deterministic rather than map-order noise.
func TestSelectAccelerator_PrefersTheMostWidelyAdvertisedThenName(t *testing.T) {
	got, ok := selectAccelerator(map[string]int{"gpu.intel.com/i915": 1, "nvidia.com/gpu": 20})
	if !ok || got != "nvidia.com/gpu" {
		t.Errorf("want the accelerator 20 nodes advertise, got %q (ok=%v)", got, ok)
	}
	for i := 0; i < 50; i++ {
		got, ok := selectAccelerator(map[string]int{"nvidia.com/gpu": 3, "amd.com/gpu": 3})
		if !ok || got != "amd.com/gpu" {
			t.Fatalf("a tie must resolve deterministically by name, got %q (ok=%v)", got, ok)
		}
	}
}

// An unrelated extended resource is not an accelerator just because it has a domain.
func TestSelectAccelerator_UnrelatedExtendedResourceIsNotAnAccelerator(t *testing.T) {
	if resource, ok := selectAccelerator(map[string]int{"example.com/widget": 9, "smarter-devices/fuse": 9}); ok {
		t.Fatalf("an unrelated extended resource must not be read as an accelerator; got %q", resource)
	}
}

func TestAcceleratorLimits_CountIsAQuantityAndFloorsAtOne(t *testing.T) {
	if got := acceleratorLimits("amd.com/gpu", 4)["amd.com/gpu"]; got != "4" {
		t.Errorf("want quantity \"4\", got %#v", got)
	}
	for _, n := range []int{0, -1} {
		if got := acceleratorLimits("amd.com/gpu", n)["amd.com/gpu"]; got != "1" {
			t.Errorf("count %d must floor at 1, got %#v", n, got)
		}
	}
	limits := acceleratorLimits("habana.ai/gaudi", 2)
	if len(limits) != 1 {
		t.Errorf("limits must ask for exactly the one accelerator resource, got %#v", limits)
	}
}

// The anti-regression assertion: the rendered scheduler contracts carry the name the
// CLUSTER supplied and no vendor of their own. Both CRs are rendered against an
// AMD-only cluster; if either renderer ever names a vendor again, "nvidia" reappears
// in its JSON and this fails.
func TestRenderedContracts_CarryOnlyTheClusterSuppliedAccelerator(t *testing.T) {
	job := &object.FinetuneJob{
		Owner:     "acme",
		Name:      "llama-3-2-1b-swift-otter",
		Namespace: "ft-acme",
		CrName:    "llama-3-2-1b-swift-otter",
		BaseModel: "meta-llama/Llama-3.2-1B-Instruct",
		Dataset:   "acme/support-tickets",
		Method:    "qlora",
		Task:      "instruct",
		Runtime:   "hanzo-transformers",
		OutputUri: "s3://hanzo-finetunes/acme/llama-3-2-1b-swift-otter/",
		GpuCount:  2,
		NumNodes:  1,
	}
	limits := acceleratorLimits("amd.com/gpu", job.GpuCount)

	for name, obj := range map[string]map[string]interface{}{
		"TrainJob":         buildTrainJobObject(job, object.Hyperparams{}, "", limits),
		"InferenceService": buildInferenceServiceObject(job, "ft-"+job.Name, acceleratorLimits("amd.com/gpu", servingAccelerators)),
	} {
		rendered, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("%s: render failed: %v", name, err)
		}
		got := string(rendered)
		if !strings.Contains(got, "amd.com/gpu") {
			t.Errorf("%s must carry the accelerator the cluster advertises: %s", name, got)
		}
		for _, vendor := range []string{"nvidia", "NVIDIA", "intel", "habana", "neuron"} {
			if strings.Contains(got, vendor) {
				t.Errorf("%s names %q on an AMD-only cluster — the hardcode is back: %s", name, vendor, got)
			}
		}
	}
}
