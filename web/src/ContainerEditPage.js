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
import * as ContainerBackend from "./backend/ContainerBackend";
import * as Setting from "./Setting";
import i18next from "i18next";


class ContainerEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      containerOwner: props.match.params.organizationName,
      containerName: props.match.params.containerName,
      container: null,
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getContainer();
  }

  getContainer() {
    ContainerBackend.getContainer(this.props.account.owner, this.state.containerName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            container: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseContainerField(key, value) {
    if (["port"].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateContainerField(key, value) {
    value = this.parseContainerField(key, value);
    const container = this.state.container;
    container[key] = value;
    this.setState({
      container: container,
    });
  }

  renderContainer() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {this.state.mode === "add" ? i18next.t("container:New Container") : i18next.t("container:Edit Container")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitContainerEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitContainerEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteContainer()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Organization"), i18next.t("general:Organization - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.owner} onChange={e => {
              this.updateContainerField("owner", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Provider"), i18next.t("general:Provider - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.provider} onChange={e => {
              this.updateContainerField("provider", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.name} onChange={e => {
              this.updateContainerField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.displayName} onChange={e => {
              this.updateContainerField("displayName", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Created time"), i18next.t("general:Created time - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={Setting.getFormattedDate(this.state.container.createdTime)} onChange={e => {
              this.updateContainerField("createdTime", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("machine:Image"), i18next.t("machine:Image - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={Setting.getFormattedDate(this.state.container.image)} onChange={e => {
              this.updateContainerField("image", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("container:Image ID"), i18next.t("container:Image ID - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.imageId} onChange={e => {
              this.updateContainerField("imageId", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("container:Command"), i18next.t("container:Command - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.command} onChange={e => {
              this.updateContainerField("command", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("container:Size RW"), i18next.t("container:Size RW - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.sizeRw} onChange={e => {
              this.updateContainerField("sizeRw", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("container:Size root FS"), i18next.t("container:Size root FS - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.sizeRootFs} onChange={e => {
              this.updateContainerField("sizeRootFs", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:State"), i18next.t("general:State - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.container.state}> {
              this.updateContainerField("state", value);
            })}>
              {
                [
                  {id: "Running", name: "Running"},
                  {id: "Stopped", name: "Stopped"},
                ].map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
              }
            </select>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Status"), i18next.t("general:Status - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.status} onChange={e => {
              this.updateContainerField("status", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("container:Ports"), i18next.t("container:Ports - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.container.ports} onChange={e => {
              this.updateContainerField("ports", e.target.value);
            }} />
          </div>
        </div>
      </div>
    );
  }

  submitContainerEdit(willExist) {
    const container = Setting.deepCopy(this.state.container);
    ContainerBackend.updateContainer(this.state.container.owner, this.state.containerName, container)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              containerName: this.state.container.name,
            });
            if (willExist) {
              this.props.history.push("/containers");
            } else {
              this.props.history.push(`/containers/${this.state.container.owner}/${encodeURIComponent(this.state.container.name)}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateContainerField("name", this.state.containerName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  deleteContainer() {
    ContainerBackend.deleteContainer(this.state.container)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/containers");
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
          this.state.container !== null ? this.renderContainer() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitContainerEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitContainerEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteContainer()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      </div>
    );
  }
}

export default ContainerEditPage;
