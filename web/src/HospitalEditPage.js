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
// import * as HospitalBackend from "./backend/HospitalBackend";
import * as Setting from "./Setting";
import i18next from "i18next";

class HospitalEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,

      hospitalName: props.match.params.hospitalName,
      hospital: null,
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getHospital();
  }

  getHospital() {
    HospitalBackend.getHospital(this.props.account.owner, this.state.hospitalName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            hospital: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseHospitalField(key, value) {
    if ([].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateHospitalField(key, value) {
    value = this.parseHospitalField(key, value);

    const hospital = this.state.hospital;
    hospital[key] = value;
    this.setState({
      hospital: hospital,
    });
  }

  renderHospital() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {this.state.mode === "add" ? i18next.t("hospital:New Hospital") : i18next.t("hospital:Edit Hospital")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitHospitalEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitHospitalEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteHospital()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.hospital.name} onChange={e => {
              this.updateHospitalField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("med:Address"), i18next.t("med:Address - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.hospital.address} onChange={e => {
              this.updateHospitalField("address", e.target.value);
            }} />
          </div>
        </div>
      </div>
    );
  }

  submitHospitalEdit(willExist) {
    const hospital = Setting.deepCopy(this.state.hospital);
    HospitalBackend.updateHospital(this.state.hospital.owner, this.state.hospitalName, hospital)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              hospitalName: this.state.hospital.name,
            });
            if (willExist) {
              this.props.history.push("/hospitals");
            } else {
              this.props.history.push(`/hospitals/${encodeURIComponent(this.state.hospital.name)}`);
            }
            // this.getHospital(true);
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateHospitalField("name", this.state.hospitalName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  deleteHospital() {
    HospitalBackend.deleteHospital(this.state.hospital)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/hospitals");
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
          this.state.hospital !== null ? this.renderHospital() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitHospitalEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitHospitalEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteHospital()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      </div>
    );
  }
}

export default HospitalEditPage;
