// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
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

import React from "react";
import * as PodBackend from "./backend/PodBackend";
import * as Setting from "./Setting";
import i18next from "i18next";


class PodEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      podOwner: props.match.params.organizationName,
      podName: props.match.params.podName,
      pod: null,
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getPod();
  }

  getPod() {
    PodBackend.getPod(this.props.account.owner, this.state.podName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            pod: res.data,
          });
        } else {
          Setting.showMessage("error", `Failed to get pod: ${res.msg}`);
        }
      });
  }

  parsePodField(key, value) {
    if (["port"].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updatePodField(key, value) {
    value = this.parsePodField(key, value);
    const pod = this.state.pod;
    pod[key] = value;
    this.setState({
      pod: pod,
    });
  }

  renderPod() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {this.state.mode === "add" ? i18next.t("pod:New Pod") : i18next.t("pod:Edit Pod")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitPodEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitPodEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deletePod()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Organization"), i18next.t("general:Organization - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.pod.owner} onChange={e => {
              this.updatePodField("owner", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Provider"), i18next.t("general:Provider - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.pod.provider} onChange={e => {
              this.updatePodField("provider", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.pod.name} onChange={e => {
              this.updatePodField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Namespace"), i18next.t("general:Namespace - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.pod.namespace} onChange={e => {
              this.updatePodField("namespace", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Created time"), i18next.t("general:Created time - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={Setting.getFormattedDate(this.state.pod.createdTime)} onChange={e => {
              this.updatePodField("createdTime", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("machine:Host IP"), i18next.t("machine:Host IP - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.pod.hostIP} onChange={e => {
              this.updatePodField("hostIP", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("machine:Private IP"), i18next.t("machine:Private IP - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.pod.podIP} onChange={e => {
              this.updatePodField("podIP", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("task:Labels"), i18next.t("task:Labels - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.pod.labels} onChange={e => {
              this.updatePodField("labels", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Status"), i18next.t("general:Status - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.pod.status}> {
              this.updatePodField("status", value);
            })}>
              {
                [
                  {id: "Running", name: "Running"},
                ].map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
              }
            </select>
          </div>
        </div>
      </div>
    );
  }

  submitPodEdit(willExist) {
    const pod = Setting.deepCopy(this.state.pod);
    PodBackend.updatePod(this.state.pod.owner, this.state.podName, pod)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              podName: this.state.pod.name,
            });
            if (willExist) {
              this.props.history.push("/pods");
            } else {
              this.props.history.push(`/pods/${this.state.pod.owner}/${encodeURIComponent(this.state.pod.name)}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updatePodField("name", this.state.podName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  deletePod() {
    PodBackend.deletePod(this.state.pod)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/pods");
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  render() {
    return (
      <div>
        {
          this.state.pod !== null ? this.renderPod() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitPodEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitPodEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deletePod()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      </div>
    );
  }
}

export default PodEditPage;
