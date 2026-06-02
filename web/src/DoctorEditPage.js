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
import * as DoctorBackend from "./backend/DoctorBackend";
import * as HospitalBackend from "./backend/HospitalBackend";
import * as Setting from "./Setting";
import i18next from "i18next";

class DoctorEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,

      doctorName: props.match.params.doctorName,
      doctor: null,
      hospitals: [],
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getDoctor();
    this.getHospitals();
  }

  getDoctor() {
    DoctorBackend.getDoctor(
      this.props.account.owner,
      this.state.doctorName
    ).then((res) => {
      if (res.status === "ok") {
        this.setState({
          doctor: res.data,
        });
      } else {
        Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
      }
    });
  }

  getHospitals() {
    HospitalBackend.getHospitals(this.props.account.owner)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            hospitals: res.data || [],
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseDoctorField(key, value) {
    if ([].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateDoctorField(key, value) {
    value = this.parseDoctorField(key, value);

    const doctor = this.state.doctor;
    doctor[key] = value;
    this.setState({
      doctor: doctor,
    });
  }

  renderDoctor() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
            {this.state.mode === "add"
              ? i18next.t("doctor:New Doctor")
              : i18next.t("doctor:Edit Doctor")}
            &nbsp;&nbsp;&nbsp;&nbsp;
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitDoctorEdit(false)}>
              {i18next.t("general:Save")}
            </button>
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitDoctorEdit(true)}
            >
              {i18next.t("general:Save & Exit")}
            </button>
            {this.state.mode === "add" ? (
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteDoctor()}
              >
                {i18next.t("general:Cancel")}
              </button>
            ) : null}
          </div>
        }
        style={{marginLeft: "5px"}}
        type="inner"
      >
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("general:Name"),
              i18next.t("general:Name - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.doctor.name}
              onChange={(e) => {
                this.updateDoctorField("name", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Department"),
              i18next.t("med:Department - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.doctor.department}
              onChange={(e) => {
                this.updateDoctorField("department", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Gender"),
              i18next.t("med:Gender - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.doctor.gender}> {
                this.updateDoctorField("gender", value);
              }}
              options={[
                {value: "Male", label: "Male"},
                {value: "Female", label: "Female"},
                {value: "Other", label: "Other"},
              ].map((item) => Setting.getOption(item.label, item.value))}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Access level"),
              i18next.t("med:Access level - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.doctor.accessLevel}
              onChange={(e) => {
                this.updateDoctorField("accessLevel", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Hospital"),
              i18next.t("med:Hospital - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.doctor.hospitalName}> {
                this.updateDoctorField("hospitalName", value);
              }}
              options={this.state.hospitals.map((hospital) => ({
                label: hospital.name,
                value: hospital.name,
              }))}
            />
          </div>
        </div>
      </div>
    );
  }

  submitDoctorEdit(willExist) {
    const doctor = Setting.deepCopy(this.state.doctor);
    DoctorBackend.updateDoctor(this.state.doctor.owner, this.state.doctorName, doctor)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              doctorName: this.state.doctor.name,
            });
            if (willExist) {
              this.props.history.push("/doctors");
            } else {
              this.props.history.push(`/doctors/${encodeURIComponent(this.state.doctor.name)}`);
            }
          // this.getDoctor(true);
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateDoctorField("name", this.state.doctorName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch((error) => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  deleteDoctor() {
    DoctorBackend.deleteDoctor(this.state.doctor)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/doctors");
        } else {
          Setting.showMessage(
            "error",
            `${i18next.t("general:Failed to delete")}: ${res.msg}`
          );
        }
      })
      .catch((error) => {
        Setting.showMessage(
          "error",
          `${i18next.t("general:Failed to connect to server")}: ${error}`
        );
      });
  }

  render() {
    return (
      <div>
        {this.state.doctor !== null ? this.renderDoctor() : null}
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitDoctorEdit(false)}>
            {i18next.t("general:Save")}
          </button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitDoctorEdit(true)}
          >
            {i18next.t("general:Save & Exit")}
          </button>
          {this.state.mode === "add" ? (
            <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteDoctor()}
            >
              {i18next.t("general:Cancel")}
            </button>
          ) : null}
        </div>
      </div>
    );
  }
}

export default DoctorEditPage;
