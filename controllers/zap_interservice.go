// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// ZAP inter-service transport for cloud operations.
//
// Listens on CLOUD_ZAP_PORT (default 9320) and handles operational
// opcodes used by Console, Platform, and Gateway to manage deployments,
// check status, and stream logs without going through HTTP.
//
// Message type 110 (cloud ops):
//   Request:  method(0:Text) + auth(8:Text) + body(16:Bytes)
//   Response: status(0:Uint32) + body(4:Bytes) + error(12:Text)

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/beego/beego/logs"
	"github.com/luxfi/zap"

	"github.com/hanzoai/cloud/object"
)

// MsgTypeCloudOps is the ZAP message type for inter-service cloud operations.
const MsgTypeCloudOps uint16 = 110

var interserviceNode *zap.Node

// InitInterserviceZap starts a dedicated ZAP node for inter-service operations.
// Separate from the main inference ZAP node (port 9999).
func InitInterserviceZap() {
	port := 9320
	if p := os.Getenv("CLOUD_ZAP_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}

	node := zap.NewNode(zap.NodeConfig{
		NodeID:      "cloud-ops",
		Port:        port,
		NoDiscovery: true,
		Logger:      slog.Default(),
	})

	if err := node.Start(); err != nil {
		logs.Error("ZAP ops: failed to start on :%d: %v", port, err)
		return
	}

	node.Handle(MsgTypeCloudOps, handleCloudOps)
	interserviceNode = node
	logs.Info("ZAP ops: listening on :%d (msg_type=%d)", port, MsgTypeCloudOps)
}

// StopInterserviceZap gracefully shuts down the inter-service ZAP node.
func StopInterserviceZap() {
	if interserviceNode != nil {
		interserviceNode.Stop()
		interserviceNode = nil
		logs.Info("ZAP ops: stopped")
	}
}

func handleCloudOps(ctx context.Context, from string, msg *zap.Message) (*zap.Message, error) {
	root := msg.Root()
	method := root.Text(object.CloudReqMethod)
	body := root.Bytes(object.CloudReqBody)

	switch method {
	case "deploy":
		return opsDeployHandler(body)
	case "undeploy":
		return opsUndeployHandler(body)
	case "status":
		return opsStatusHandler()
	case "logs":
		return opsLogsHandler(body)
	case "pods":
		return opsPodsHandler(body)
	case "containers":
		return opsContainersHandler(body)
	default:
		return buildOpsResponse(404, nil, "unknown op: "+method)
	}
}

func buildOpsResponse(status uint32, body []byte, errMsg string) (*zap.Message, error) {
	return object.BuildCloudResponse(status, body, errMsg)
}

// ── deploy ──────────────────────────────────────────────────────────────

func opsDeployHandler(body []byte) (*zap.Message, error) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		return buildOpsResponse(400, nil, "invalid body: "+err.Error())
	}
	if params.ID == "" {
		return buildOpsResponse(400, nil, "id required")
	}

	app, err := object.GetApplication(params.ID)
	if err != nil {
		return buildOpsResponse(500, nil, err.Error())
	}
	if app == nil {
		return buildOpsResponse(404, nil, "application not found: "+params.ID)
	}

	ok, err := object.DeployApplicationSync(app, "en")
	if err != nil {
		return buildOpsResponse(500, nil, "deploy failed: "+err.Error())
	}
	if !ok {
		return buildOpsResponse(500, nil, "deploy returned false")
	}

	updated, err := object.GetApplication(params.ID)
	if err != nil {
		return buildOpsResponse(500, nil, err.Error())
	}

	data, _ := json.Marshal(updated)
	return buildOpsResponse(200, data, "")
}

// ── undeploy ────────────────────────────────────────────────────────────

func opsUndeployHandler(body []byte) (*zap.Message, error) {
	var params struct {
		Owner     string `json:"owner"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		return buildOpsResponse(400, nil, "invalid body: "+err.Error())
	}
	if params.Owner == "" || params.Name == "" {
		return buildOpsResponse(400, nil, "owner and name required")
	}

	ok, err := object.UndeployApplicationSync(params.Owner, params.Name, params.Namespace, "en")
	if err != nil {
		return buildOpsResponse(500, nil, "undeploy failed: "+err.Error())
	}
	if !ok {
		return buildOpsResponse(500, nil, "undeploy returned false")
	}

	data, _ := json.Marshal(map[string]string{"status": "undeployed", "owner": params.Owner, "name": params.Name})
	return buildOpsResponse(200, data, "")
}

// ── status ──────────────────────────────────────────────────────────────

func opsStatusHandler() (*zap.Message, error) {
	status, err := object.GetK8sStatus("en")
	if err != nil {
		return buildOpsResponse(500, nil, err.Error())
	}

	data, _ := json.Marshal(map[string]interface{}{"k8s": status})
	return buildOpsResponse(200, data, "")
}

// ── logs ────────────────────────────────────────────────────────────────

func opsLogsHandler(body []byte) (*zap.Message, error) {
	var params struct {
		Owner     string `json:"owner"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		TailLines int    `json:"tailLines"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		return buildOpsResponse(400, nil, "invalid body: "+err.Error())
	}

	id := params.Owner + "/" + params.Name
	if id == "/" {
		return buildOpsResponse(400, nil, "owner and name required")
	}

	pod, err := object.GetPod(id)
	if err != nil {
		return buildOpsResponse(500, nil, err.Error())
	}
	if pod == nil {
		return buildOpsResponse(404, nil, "pod not found: "+id)
	}

	data, _ := json.Marshal(pod)
	return buildOpsResponse(200, data, "")
}

// ── pods ────────────────────────────────────────────────────────────────

func opsPodsHandler(body []byte) (*zap.Message, error) {
	var params struct {
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		return buildOpsResponse(400, nil, "invalid body: "+err.Error())
	}
	if params.Owner == "" {
		return buildOpsResponse(400, nil, "owner required")
	}

	pods, err := object.GetPods(params.Owner)
	if err != nil {
		return buildOpsResponse(500, nil, err.Error())
	}

	data, _ := json.Marshal(pods)
	return buildOpsResponse(200, data, "")
}

// ── containers ──────────────────────────────────────────────────────────

func opsContainersHandler(body []byte) (*zap.Message, error) {
	var params struct {
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		return buildOpsResponse(400, nil, "invalid body: "+err.Error())
	}
	if params.Owner == "" {
		return buildOpsResponse(400, nil, "owner required")
	}

	containers, err := object.GetContainers(params.Owner)
	if err != nil {
		return buildOpsResponse(500, nil, err.Error())
	}

	data, _ := json.Marshal(containers)
	return buildOpsResponse(200, data, "")
}
