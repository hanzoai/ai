// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
package object
import (
	"fmt"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	ecs20140526 "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)
type Machine struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	Id          string `json:"id"`
	Provider    string `json:"provider"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	ExpireTime  string `json:"expireTime"`
	DisplayName string `json:"displayName"`
	Region   string `json:"region"`
	Zone     string `json:"zone"`
	Category string `json:"category"`
	Type     string `json:"type"`
	Size     string `json:"size"`
	Tag      string `json:"tag"`
	State    string `json:"state"`
	Image     string `json:"image"`
	Os        string `json:"os"`
	PublicIp  string `json:"publicIp"`
	PrivateIp string `json:"privateIp"`
	CpuSize   string `json:"cpuSize"`
	MemSize   string `json:"memSize"`
	// DB info
	RemoteProtocol string `json:"remoteProtocol"`
	RemotePort     int    `json:"remotePort"`
	RemoteUsername string `json:"remoteUsername"`
	RemotePassword string `json:"remotePassword"`
}
func GetMachineCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "machine")
}
func GetMachines(owner string) ([]*Machine, error) {
	machines := []*Machine{}
	err := findAll(adapter.db, "machine", &machines, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return machines, err
	}
	return machines, nil
}
func GetPaginationMachines(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Machine, error) {
	machines := []*Machine{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "machine", &machines)
	if err != nil {
		return machines, err
	}
	return machines, nil
}
func getMachine(owner string, name string) (*Machine, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	machine := Machine{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "machine", &machine, pk2(machine.Owner, machine.Name))
	if err != nil {
		return &machine, err
	}
	if existed {
		return &machine, nil
	} else {
		return nil, nil
	}
}
func GetMachine(id string) (*Machine, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getMachine(owner, name)
}
func GetMaskedMachine(machine *Machine, errs ...error) (*Machine, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	if machine == nil {
		return nil, nil
	}
	if machine.RemotePassword != "" {
		machine.RemotePassword = "***"
	}
	return machine, nil
}
func GetMaskedMachines(machines []*Machine, errs ...error) ([]*Machine, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	var err error
	for _, machine := range machines {
		machine, err = GetMaskedMachine(machine)
		if err != nil {
			return nil, err
		}
	}
	return machines, nil
}
func UpdateMachine(id string, machine *Machine) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	oldMachine, err := getMachine(owner, name)
	if err != nil {
		return false, err
	} else if oldMachine == nil {
		return false, nil
	}
	if machine.RemotePassword == "***" {
		machine.RemotePassword = oldMachine.RemotePassword
	}
	_, err = updateMachineCloud(oldMachine, machine, "en")
	if err != nil {
		return false, err
	}
	machine.Owner = owner
	machine.Name = name
	err = adapter.db.Model(machine).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func AddMachine(machine *Machine) (bool, error) {
	if len(machine.DisplayName) > 0 {
		res, err := createMachineByImage(machine)
		if err != nil || res == false {
			return false, err
		}
	}
	err := insertRow(adapter.db, machine)
	if err != nil {
		return false, err
	}
	return true, nil
}
func createMachineByImage(machine *Machine) (bool, error) {
	providers, err := getActiveCloudProviders(machine.Owner)
	if err != nil {
		return false, err
	}
	for _, provider := range providers {
		if provider.Type == "Aliyun" {
			config := &openapi.Config{
				AccessKeyId:     tea.String(provider.ClientId),
				AccessKeySecret: tea.String(provider.ClientSecret),
				RegionId:        tea.String(provider.Region),
				Endpoint:        tea.String("ecs." + provider.Region + ".aliyuncs.com"),
			}
			client, err2 := ecs20140526.NewClient(config)
			if err2 != nil {
				return false, err2
			}
			request0 := &ecs20140526.DescribeAvailableResourceRequest{
				RegionId:            tea.String(provider.Region),
				DestinationResource: tea.String("InstanceType"),
			}
			response0, err2 := client.DescribeAvailableResource(request0)
			if err2 != nil {
				return false, err2
			}
			supportedResource := response0.Body.AvailableZones.AvailableZone[0].AvailableResources.AvailableResource[0].SupportedResources.SupportedResource
			var instanceType string
			for _, resource := range supportedResource {
				if tea.StringValue(resource.Status) == "Available" {
					instanceType = tea.StringValue(resource.Value)
					break
				}
			}
			request1 := &ecs20140526.DescribeSecurityGroupsRequest{}
			response1, err2 := client.DescribeSecurityGroups(request1)
			if err2 != nil {
				return false, err2
			}
			securityGroupId := tea.StringValue(response1.Body.SecurityGroups.SecurityGroup[0].SecurityGroupId)
			vpcId := tea.StringValue(response1.Body.SecurityGroups.SecurityGroup[0].VpcId)
			request2 := &ecs20140526.DescribeVSwitchesRequest{
				VpcId:    tea.String(vpcId),
				RegionId: tea.String(provider.Region),
			}
			response2, err2 := client.DescribeVSwitches(request2)
			if err2 != nil {
				return false, err2
			}
			vSwitchId := tea.StringValue(response2.Body.VSwitches.VSwitch[0].VSwitchId)
			request3 := &ecs20140526.DescribeAvailableResourceRequest{
				RegionId:            tea.String(provider.Region),
				DestinationResource: tea.String("SystemDisk"),
				InstanceType:        tea.String(instanceType),
			}
			response3, err3 := client.DescribeAvailableResource(request3)
			if err3 != nil {
				return false, err3
			}
			systemDiskCategory := tea.StringValue(response3.Body.AvailableZones.AvailableZone[0].AvailableResources.AvailableResource[0].SupportedResources.SupportedResource[0].Value)
			request := &ecs20140526.RunInstancesRequest{
				InstanceType:    tea.String(instanceType),
				RegionId:        tea.String(provider.Region),
				ImageId:         tea.String(machine.DisplayName),
				SecurityGroupId: tea.String(securityGroupId),
				VSwitchId:       tea.String(vSwitchId),
				SystemDisk: &ecs20140526.RunInstancesRequestSystemDisk{
					Category: tea.String(systemDiskCategory),
				},
			}
			_, err := client.RunInstances(request)
			if err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}
func addMachines(machines []*Machine) (bool, error) {
	err := insertRow(adapter.db, machines)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteMachine(machine *Machine) (bool, error) {
	affected, err := deleteByPK(adapter.db, "machine", pk2(machine.Owner, machine.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func deleteMachines(owner string) (bool, error) {
	affected, err := deleteWhere(adapter.db, "machine", dbx.HashExp{"owner": owner})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func (machine *Machine) GetId() string {
	return fmt.Sprintf("%s/%s", machine.Owner, machine.Name)
}
