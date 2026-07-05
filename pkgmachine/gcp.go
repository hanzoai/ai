// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

package pkgmachine

import (
	"context"
	"fmt"

	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

type MachineGcpClient struct {
	Service   *compute.Service
	ProjectID string
	Zone      string
}

// newMachineGcpClient builds a compute client over the REST transport
// (google.golang.org/api, JSON/HTTP). credentialsJSON is the service-account
// key; when empty, Application Default Credentials are used (in-cluster
// workload identity). projectID and zone scope every instance call.
func newMachineGcpClient(projectID string, credentialsJSON string, zone string) (MachineGcpClient, error) {
	ctx := context.Background()

	var opts []option.ClientOption
	if credentialsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(credentialsJSON)))
	}

	service, err := compute.NewService(ctx, opts...)
	if err != nil {
		return MachineGcpClient{}, err
	}

	return MachineGcpClient{Service: service, ProjectID: projectID, Zone: zone}, nil
}

func getMachineFromComputeInstance(instance *compute.Instance) *Machine {
	machine := &Machine{
		Name:        instance.Name,
		Id:          fmt.Sprintf("%d", instance.Id),
		CreatedTime: getLocalTimestamp(instance.CreationTimestamp),
		UpdatedTime: getLocalTimestamp(instance.LastStartTimestamp),
		DisplayName: instance.Name,
		Zone:        instance.Zone,
		Type:        instance.MachineType,
		State:       instance.Status,
		Image:       instance.SourceMachineImage,
		CpuSize:     instance.CpuPlatform,
	}

	if len(instance.Disks) > 0 && len(instance.Disks[0].GuestOsFeatures) > 0 {
		machine.Os = instance.Disks[0].GuestOsFeatures[0].Type
	}

	for key, value := range instance.Labels {
		machine.Tag += fmt.Sprintf("%s=%s,", key, value)
	}

	if len(instance.NetworkInterfaces) > 0 {
		if len(instance.NetworkInterfaces[0].AccessConfigs) > 0 {
			machine.PublicIp = instance.NetworkInterfaces[0].AccessConfigs[0].NatIP
		}
		machine.PrivateIp = instance.NetworkInterfaces[0].NetworkIP
	}

	return machine
}

func (client MachineGcpClient) GetMachines(lang string) ([]*Machine, error) {
	ctx := context.Background()
	resp, err := client.Service.Instances.List(client.ProjectID, client.Zone).MaxResults(100).Context(ctx).Do()
	if err != nil {
		return nil, err
	}

	machines := []*Machine{}
	for _, instance := range resp.Items {
		machine := getMachineFromComputeInstance(instance)
		machines = append(machines, machine)
	}

	return machines, nil
}

func (client MachineGcpClient) GetMachine(name string, lang string) (*Machine, error) {
	ctx := context.Background()
	instance, err := client.Service.Instances.Get(client.ProjectID, client.Zone, name).Context(ctx).Do()
	if err != nil {
		return nil, err
	}

	machine := getMachineFromComputeInstance(instance)
	return machine, nil
}

func (client MachineGcpClient) UpdateMachineState(name string, state string, lang string) (bool, string, error) {
	machine, err := client.GetMachine(name, lang)
	if err != nil {
		return false, "", err
	}

	if machine == nil {
		return false, fmt.Sprintf("Instance: [%s] is not found", name), nil
	}

	instanceId := machine.Id

	switch state {
	case "Running":
		_, err = client.Service.Instances.Start(client.ProjectID, client.Zone, instanceId).Context(context.Background()).Do()
	case "Stopped":
		_, err = client.Service.Instances.Stop(client.ProjectID, client.Zone, instanceId).Context(context.Background()).Do()
	default:
		return false, fmt.Sprintf("Unsupported state: %s", state), nil
	}

	if err != nil {
		return false, "", err
	}

	return true, fmt.Sprintf("Instance: [%s]'s state has been successfully updated to: [%s]", name, state), nil
}
