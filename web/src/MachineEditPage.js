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
import * as MachineBackend from "./backend/MachineBackend";
import * as Setting from "./Setting";
import i18next from "i18next";


class MachineEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      machineOwner: props.match.params.organizationName,
      machineName: props.match.params.machineName,
      machine: null,
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getMachine();
  }

  getMachine() {
    MachineBackend.getMachine(this.props.account.owner, this.state.machineName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            machine: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseMachineField(key, value) {
    if (["port"].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateMachineField(key, value) {
    value = this.parseMachineField(key, value);
    const machine = this.state.machine;
    machine[key] = value;
    this.setState({
      machine: machine,
    });
  }

  renderMachine() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {this.state.mode === "add" ? i18next.t("machine:New Machine") : i18next.t("machine:Edit Machine")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitMachineEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitMachineEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteMachine()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Organization"), i18next.t("general:Organization - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.owner} onChange={e => {
              this.updateMachineField("owner", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.name} onChange={e => {
              this.updateMachineField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Provider"), i18next.t("general:Provider - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.provider} onChange={e => {
              this.updateMachineField("provider", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.displayName} onChange={e => {
              this.updateMachineField("displayName", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Created time"), i18next.t("general:Created time - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={Setting.getFormattedDate(this.state.machine.createdTime)} onChange={e => {
              this.updateMachineField("createdTime", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Expire time"), i18next.t("general:Expire time - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={Setting.getFormattedDate(this.state.machine.expireTime)} onChange={e => {
              this.updateMachineField("expireTime", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Region"), i18next.t("general:Region - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.region} onChange={e => {
              this.updateMachineField("region", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Zone"), i18next.t("general:Zone - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.zone} onChange={e => {
              this.updateMachineField("zone", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Category"), i18next.t("provider:Category - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.category} onChange={e => {
              this.updateMachineField("category", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Type"), i18next.t("general:Type - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.machine.type}> {
              this.updateMachineField("type", value);
            })}>
              {
                [
                  {id: "Pay As You Go", name: "Pay As You Go"},
                  {id: "Reserved", name: "Reserved"},
                ].map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
              }
            </select>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Size"), i18next.t("general:Size - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.size} onChange={e => {
              this.updateMachineField("size", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("machine:Image"), i18next.t("machine:Image - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.image} onChange={e => {
              this.updateMachineField("image", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("machine:Public IP"), i18next.t("machine:Public IP - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.publicIp} onChange={e => {
              this.updateMachineField("publicIp", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("machine:Private IP"), i18next.t("machine:Private IP - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.privateIp} onChange={e => {
              this.updateMachineField("privateIp", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("machine:Protocol"), i18next.t("machine:Protocol - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.machine.remoteProtocol}> {
              this.updateMachineField("remoteProtocol", value);
            }}
            options={[
              {value: "SSH", label: "SSH"},
              {value: "RDP", label: "RDP"},
              {value: "Telnet", label: "Telnet"},
              {value: "VNC", label: "VNC"},
            ].map(item => Setting.getOption(item.label, item.value))} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("machine:Port"), i18next.t("machine:Port - Tooltip"))} :
          </div>
          <div className="flex-1">
            <InputNumber value={this.state.machine.remotePort} min={0} max={65535} step={1} onChange={value => {
              this.updateMachineField("remotePort", value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Username"), i18next.t("general:Username - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.remoteUsername} onChange={e => {
              this.updateMachineField("remoteUsername", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Password"), i18next.t("general:Password - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.machine.remotePassword} onChange={e => {
              this.updateMachineField("remotePassword", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:State"), i18next.t("general:State - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.machine.state}> {
              this.updateMachineField("state", value);
            }}
            options={[
              {value: "Running", label: "Running"},
              {value: "Stopped", label: "Stopped"},
            ].map(item => Setting.getOption(item.label, item.value))} />
          </div>
        </div>
      </div>
    );
  }

  submitMachineEdit(willExist) {
    const machine = Setting.deepCopy(this.state.machine);
    MachineBackend.updateMachine(this.state.machine.owner, this.state.machineName, machine)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              machineName: this.state.machine.name,
            });
            if (willExist) {
              this.props.history.push("/machines");
            } else {
              this.props.history.push(`/machines/${this.state.machine.owner}/${encodeURIComponent(this.state.machine.name)}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
            this.updateMachineField("name", this.state.machineName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  deleteMachine() {
    MachineBackend.deleteMachine(this.state.machine)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/machines");
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
          this.state.machine !== null ? this.renderMachine() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitMachineEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitMachineEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteMachine()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      </div>
    );
  }
}

export default MachineEditPage;
