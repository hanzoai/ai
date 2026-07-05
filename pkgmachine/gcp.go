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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	computeBaseURL = "https://compute.googleapis.com/compute/v1"
	computeScope   = "https://www.googleapis.com/auth/compute"
)

type MachineGcpClient struct {
	httpClient *http.Client
	projectID  string
	zone       string
}

// computeInstance is the subset of the compute REST instance resource this
// adapter maps. JSON over HTTP — no SDK, no gRPC.
type computeInstance struct {
	Name               string                `json:"name"`
	Id                 string                `json:"id"`
	CreationTimestamp  string                `json:"creationTimestamp"`
	LastStartTimestamp string                `json:"lastStartTimestamp"`
	Zone               string                `json:"zone"`
	MachineType        string                `json:"machineType"`
	Status             string                `json:"status"`
	SourceMachineImage string                `json:"sourceMachineImage"`
	CpuPlatform        string                `json:"cpuPlatform"`
	Labels             map[string]string     `json:"labels"`
	Disks              []computeDisk         `json:"disks"`
	NetworkInterfaces  []computeNetworkIface `json:"networkInterfaces"`
}

type computeDisk struct {
	GuestOsFeatures []computeGuestOsFeature `json:"guestOsFeatures"`
}

type computeGuestOsFeature struct {
	Type string `json:"type"`
}

type computeNetworkIface struct {
	NetworkIP     string                `json:"networkIP"`
	AccessConfigs []computeAccessConfig `json:"accessConfigs"`
}

type computeAccessConfig struct {
	NatIP string `json:"natIP"`
}

type computeInstanceList struct {
	Items []computeInstance `json:"items"`
}

// gcpTokenSource returns an OAuth2 token source over pure HTTP token exchange:
// the service-account JSON when supplied, else Application Default Credentials
// (in-cluster workload identity). No gRPC is involved.
func gcpTokenSource(ctx context.Context, credentialsJSON string) (oauth2.TokenSource, error) {
	if credentialsJSON != "" {
		cfg, err := google.JWTConfigFromJSON([]byte(credentialsJSON), computeScope)
		if err != nil {
			return nil, err
		}
		return cfg.TokenSource(ctx), nil
	}
	return google.DefaultTokenSource(ctx, computeScope)
}

func newMachineGcpClient(projectID string, credentialsJSON string, zone string) (MachineGcpClient, error) {
	ctx := context.Background()
	ts, err := gcpTokenSource(ctx, credentialsJSON)
	if err != nil {
		return MachineGcpClient{}, err
	}
	return MachineGcpClient{httpClient: oauth2.NewClient(ctx, ts), projectID: projectID, zone: zone}, nil
}

// do issues an authenticated JSON request and decodes a 2xx body into out
// (out may be nil). A non-2xx response is a hard error with a bounded body.
func (client MachineGcpClient) do(ctx context.Context, method string, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("compute API %s %s: %s", method, resp.Status, string(body))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (client MachineGcpClient) instancesURL() string {
	return fmt.Sprintf("%s/projects/%s/zones/%s/instances", computeBaseURL, client.projectID, client.zone)
}

func getMachineFromComputeInstance(instance *computeInstance) *Machine {
	machine := &Machine{
		Name:        instance.Name,
		Id:          instance.Id,
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
	endpoint := fmt.Sprintf("%s?maxResults=100", client.instancesURL())

	var list computeInstanceList
	if err := client.do(ctx, http.MethodGet, endpoint, &list); err != nil {
		return nil, err
	}

	machines := []*Machine{}
	for i := range list.Items {
		machines = append(machines, getMachineFromComputeInstance(&list.Items[i]))
	}

	return machines, nil
}

func (client MachineGcpClient) GetMachine(name string, lang string) (*Machine, error) {
	ctx := context.Background()
	endpoint := fmt.Sprintf("%s/%s", client.instancesURL(), url.PathEscape(name))

	var instance computeInstance
	if err := client.do(ctx, http.MethodGet, endpoint, &instance); err != nil {
		return nil, err
	}

	return getMachineFromComputeInstance(&instance), nil
}

func (client MachineGcpClient) UpdateMachineState(name string, state string, lang string) (bool, string, error) {
	machine, err := client.GetMachine(name, lang)
	if err != nil {
		return false, "", err
	}

	if machine == nil {
		return false, fmt.Sprintf("Instance: [%s] is not found", name), nil
	}

	var action string
	switch state {
	case "Running":
		action = "start"
	case "Stopped":
		action = "stop"
	default:
		return false, fmt.Sprintf("Unsupported state: %s", state), nil
	}

	ctx := context.Background()
	endpoint := fmt.Sprintf("%s/%s/%s", client.instancesURL(), url.PathEscape(name), action)
	if err := client.do(ctx, http.MethodPost, endpoint, nil); err != nil {
		return false, "", err
	}

	return true, fmt.Sprintf("Instance: [%s]'s state has been successfully updated to: [%s]", name, state), nil
}
