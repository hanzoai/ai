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
package object

import (
	"fmt"

	"github.com/hanzoai/cloud/i18n"
	"github.com/hanzoai/cloud/pkgkubernetes"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

type Pod struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	Provider    string `json:"provider"`
	Namespace   string `json:"namespace"`
	CreatedTime string `json:"createdTime"`
	HostIP      string `json:"hostIP"`
	PodIP       string `json:"podIP"`
	Labels      string `json:"labels"`
	Status      string `json:"status"`
}

func GetPodCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "pod")
}

func GetPods(owner string) ([]*Pod, error) {
	pods := []*Pod{}
	err := findAll(adapter.db, "pod", &pods, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return pods, err
	}
	return pods, nil
}

func GetPaginationPods(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Pod, error) {
	pods := []*Pod{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "pod", &pods)
	if err != nil {
		return pods, err
	}
	return pods, nil
}

func getPod(owner string, name string) (*Pod, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	pod := Pod{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "pod", &pod, pk2(pod.Owner, pod.Name))
	if err != nil {
		return &pod, err
	}
	if existed {
		return &pod, nil
	} else {
		return nil, nil
	}
}

func GetPod(id string) (*Pod, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getPod(owner, name)
}

func GetMaskedPod(pod *Pod, errs ...error) (*Pod, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	if pod == nil {
		return nil, nil
	}
	return pod, nil
}

func GetMaskedPods(pods []*Pod, errs ...error) ([]*Pod, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	var err error
	for _, pod := range pods {
		pod, err = GetMaskedPod(pod)
		if err != nil {
			return nil, err
		}
	}
	return pods, nil
}

func UpdatePod(id string, pod *Pod, lang string) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	oldPod, err := getPod(owner, name)
	if err != nil {
		return false, err
	} else if oldPod == nil {
		return false, nil
	}
	_, err = updatePod(oldPod, pod, lang)
	if err != nil {
		return false, err
	}
	pod.Owner = owner
	pod.Name = name
	err = adapter.db.Model(pod).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func AddPod(pod *Pod) (bool, error) {
	err := insertRow(adapter.db, pod)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func addPods(pods []*Pod) (bool, error) {
	err := insertRow(adapter.db, pods)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeletePod(pod *Pod) (bool, error) {
	affected, err := deleteByPK(adapter.db, "pod", pk2(pod.Owner, pod.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func deletePods(owner string) (bool, error) {
	affected, err := deleteWhere(adapter.db, "pod", dbx.HashExp{"owner": owner})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (pod *Pod) GetId() string {
	return fmt.Sprintf("%s/%s", pod.Owner, pod.Name)
}

func SyncKubernetesPods(owner string) (bool, error) {
	pods, err := getPods(owner)
	if err != nil {
		return false, err
	}
	dbPods, err := GetPods(owner)
	if err != nil {
		return false, err
	}
	dbPodMap := map[string]*Pod{}
	for _, dbPod := range dbPods {
		dbPodMap[dbPod.GetId()] = dbPod
	}
	_, err = deletePods(owner)
	if err != nil {
		return false, err
	}
	if len(pods) == 0 {
		return false, nil
	}
	affected, err := addPods(pods)
	return affected, err
}

func updatePod(oldPod *Pod, pod *Pod, lang string) (bool, error) {
	provider, err := getProvider("admin", oldPod.Provider)
	if err != nil {
		return false, err
	}
	if provider == nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The provider: %s does not exist"), pod.Provider))
	}
	client, err := pkgkubernetes.NewPodClient(provider.Type, provider.ClientId, provider.ClientSecret, provider.Region)
	if err != nil {
		return false, err
	}
	if oldPod.Status != pod.Status {
		affected, _, err := client.UpdatePodState(oldPod.Name, pod.Status)
		if err != nil {
			return false, err
		}
		return affected, nil
	}
	return false, nil
}

func getPods(owner string) ([]*Pod, error) {
	pods := []*Pod{}
	providers, err := GetProviders("admin")
	if err != nil {
		return nil, err
	}
	for _, provider := range providers {
		if provider.Category == "Kubernetes" {
			client, err2 := pkgkubernetes.NewPodClient(provider.Type, provider.ClientId, provider.ClientSecret, provider.Region)
			if err2 != nil {
				return nil, err2
			}
			clientPods, err2 := client.GetPods()
			for _, clientPod := range clientPods {
				pod := getPodFromService(owner, provider.Name, clientPod)
				pods = append(pods, pod)
			}
		}
	}
	return pods, nil
}

func getPodFromService(owner string, provider string, clientPod *pkgkubernetes.Pod) *Pod {
	return &Pod{
		Owner:       owner,
		Name:        clientPod.Name,
		Provider:    provider,
		Namespace:   clientPod.Namespace,
		CreatedTime: clientPod.CreatedTime,
		HostIP:      clientPod.HostIP,
		PodIP:       clientPod.PodIP,
		Labels:      clientPod.Labels,
		Status:      clientPod.Status,
	}
}
