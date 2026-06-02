// Copyright 2023 Hanzo AI Inc.. All Rights Reserved.
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
import * as RecordBackend from "./backend/RecordBackend";
import * as ProviderBackend from "./backend/ProviderBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import Editor from "./common/Editor";


class RecordEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      recordOwner: props.match.params.organizationName,
      recordName: props.match.params.recordName,
      record: null,
      blockchainProviders: [],
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getRecord();
    this.getProviders();
  }

  getRecord() {
    RecordBackend.getRecord(this.props.account.owner, this.state.recordName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            record: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getProviders() {
    ProviderBackend.getProviders(this.props.account.owner)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            blockchainProviders: res.data.filter(provider => provider.category === "Blockchain" && provider.state === "Active"),
          });
        } else {
          Setting.showMessage("error", res.msg);
        }
      });
  }

  parseRecordField(key, value) {
    if (["count"].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateRecordField(key, value) {
    value = this.parseRecordField(key, value);

    const record = this.state.record;
    record[key] = value;
    this.setState({
      record: record,
    });
  }

  renderRecord() {
    // const history = useHistory();
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {this.state.mode === "add" ? i18next.t("record:New Record") : i18next.t("record:View Record")}&nbsp;&nbsp;&nbsp;&nbsp;
          {this.state.mode !== "123" ? (
            <React.Fragment>
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitRecordEdit(false)}>{i18next.t("general:Save")}</button>
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitRecordEdit(true)}>{i18next.t("general:Save & Exit")}</button>
            </React.Fragment>
          ) : (
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200"> this.props.history.push("/records")}>{i18next.t("general:Exit")}</button>
          )}
          {this.state.mode === "add" ? <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteRecord()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Organization"), i18next.t("general:Organization - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.owner} onChange={e => {
              // this.updateRecordField("owner", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.name} onChange={e => {
              // this.updateRecordField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Provider"), i18next.t("general:Provider - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.record.provider} disabled> {
              this.updateRecordField("provider", value);
            })}>
              {
                this.state.blockchainProviders.map((provider, index) => (
                  <option key={index} value={provider.name}>{provider.name}</option>
                ))
              }
            </select>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Block"), i18next.t("general:Block - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.block} onChange={e => {
              // this.updateRecordField("block", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Provider 2"), i18next.t("general:Provider - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.record.provider2} disabled> {
              this.updateRecordField("provider2", value);
            })}>
              {
                this.state.blockchainProviders.map((provider, index) => (
                  <option key={index} value={provider.name}>{provider.name}</option>
                ))
              }
            </select>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Block 2"), i18next.t("general:Block - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.block2} onChange={e => {
              // this.updateRecordField("block", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Client IP"), i18next.t("general:Client IP - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.clientIp} onChange={e => {
              // this.updateRecordField("clientIp", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:User"), i18next.t("general:User - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.user} onChange={e => {
              // this.updateRecordField("user", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Method"), i18next.t("general:Method - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.record.method} disabled> {
              // this.updateRecordField("method", value);
            })}>
              {
                [
                  {id: "GET", name: "GET"},
                  {id: "HEAD", name: "HEAD"},
                  {id: "POST", name: "POST"},
                  {id: "PUT", name: "PUT"},
                  {id: "DELETE", name: "DELETE"},
                  {id: "CONNECT", name: "CONNECT"},
                  {id: "OPTIONS", name: "OPTIONS"},
                  {id: "TRACE", name: "TRACE"},
                  {id: "PATCH", name: "PATCH"},
                ].map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
              }
            </select>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Request URI"), i18next.t("general:Request URI - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.requestUri} onChange={e => {
              // this.updateRecordField("requestUri", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Action"), i18next.t("general:Action - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.action} onChange={e => {
              // this.updateRecordField("action", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Language"), i18next.t("general:Language - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.language} onChange={e => {
              // this.updateRecordField("language", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Region"), i18next.t("general:Region - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.record.region} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:City"), i18next.t("general:City - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.record.city} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Unit"), i18next.t("general:Unit - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.record.unit} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Section"), i18next.t("general:Section - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.record.section} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Count"), i18next.t("general:Count - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled={false} value={this.state.record.count === 0 ? 1 : this.state.record.count} onChange={e => {
              this.updateRecordField("count", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Object"), i18next.t("general:Object - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div style={{width: "900px", height: "300px"}}>
              <Editor
                value={Setting.formatJsonString(this.state.record.object)}
                lang="js"
                fillHeight
                dark
              />
            </div>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Response"), i18next.t("general:Response - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div style={{width: "900px", height: "300px"}}>
              <Editor
                value={Setting.formatJsonString(this.state.record.response)}
                lang="js"
                fillHeight
                dark
              />
            </div>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Is triggered"), i18next.t("general:Is triggered - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="px-2 py-0.5 rounded text-xs " + (this.state.record.isTriggered ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{this.state.record.isTriggered ? "ON" : "OFF"}</span>
          </div>
        </div>
      </div>
    );
  }

  submitRecordEdit(willExist) {
    const record = Setting.deepCopy(this.state.record);
    RecordBackend.updateRecord(this.state.record.owner, this.state.recordName, record)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              recordName: this.state.record.name,
            });
            if (willExist) {
              this.props.history.push("/records");
            } else {
              this.props.history.push(`/records/${this.state.record.owner}/${encodeURIComponent(this.state.record.id)}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
            this.updateRecordField("name", this.state.recordName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  deleteRecord() {
    RecordBackend.deleteRecord(this.state.record)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/records");
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
          this.state.record !== null ? this.renderRecord() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          {this.state.mode !== "123" ? (
            <React.Fragment>
              <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitRecordEdit(false)}>{i18next.t("general:Save")}</button>
              <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitRecordEdit(true)}>{i18next.t("general:Save & Exit")}</button>
            </React.Fragment>
          ) : (
            <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200"> this.props.history.push("/records")}>{i18next.t("general:Exit")}</button>
          )}
          {this.state.mode === "add" ? <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteRecord()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      </div>
    );
  }
}

export default RecordEditPage;
