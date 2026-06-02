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
import * as CaaseBackend from "./backend/CaaseBackend";
import * as PatientBackend from "./backend/PatientBackend";
import * as DoctorBackend from "./backend/DoctorBackend";
import * as Setting from "./Setting";
import i18next from "i18next";

class CaaseEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,

      caaseName: props.match.params.caaseName,
      caase: null,
      patients: [],
      doctors: [],
      hospitals: [],
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getCaase();
    this.getPatients();
    this.getDoctors();
    this.getHospitals();
  }

  getCaase() {
    CaaseBackend.getCaase(this.props.account.owner, this.state.caaseName).then(
      (res) => {
        if (res.status === "ok") {
          this.setState({
            caase: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      }
    );
  }

  getPatients() {
    PatientBackend.getPatients(this.props.account.owner)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            patients: res.data || [],
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getDoctors() {
    DoctorBackend.getDoctors(this.props.account.owner)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            doctors: res.data || [],
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getHospitals() {
    const HospitalBackend = require("./backend/HospitalBackend");
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

  parseCaaseField(key, value) {
    if ([].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateCaaseField(key, value) {
    value = this.parseCaaseField(key, value);

    const caase = this.state.caase;
    caase[key] = value;
    this.setState({
      caase: caase,
    });
  }

  renderCaase() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
            {this.state.mode === "add"
              ? i18next.t("caase:New Caase")
              : i18next.t("caase:Edit Caase")}
            &nbsp;&nbsp;&nbsp;&nbsp;
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitCaaseEdit(false)}>
              {i18next.t("general:Save")}
            </button>
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitCaaseEdit(true)}
            >
              {i18next.t("general:Save & Exit")}
            </button>
            {this.state.mode === "add" ? (
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteCaase()}
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
              value={this.state.caase.name}
              onChange={(e) => {
                this.updateCaaseField("name", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Symptoms"),
              i18next.t("med:Symptoms - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.symptoms}
              onChange={(e) => {
                this.updateCaaseField("symptoms", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Diagnosis"),
              i18next.t("med:Diagnosis - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.diagnosis}
              onChange={(e) => {
                this.updateCaaseField("diagnosis", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Diagnosis date"),
              i18next.t("med:Diagnosis date - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.diagnosisDate}
              onChange={(e) => {
                this.updateCaaseField("diagnosisDate", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Prescription"),
              i18next.t("med:Prescription - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.prescription}
              onChange={(e) => {
                this.updateCaaseField("prescription", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Follow up"),
              i18next.t("med:Follow up - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.followUp}
              onChange={(e) => {
                this.updateCaaseField("followUp", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:HIS interface info"),
              i18next.t("med:HIS interface info - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.hisInterfaceInfo}
              onChange={(e) => {
                this.updateCaaseField("hisInterfaceInfo", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Primary care physician"),
              i18next.t("med:Primary care physician - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.primaryCarePhysician}
              onChange={(e) => {
                this.updateCaaseField("primaryCarePhysician", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("general:Type"),
              i18next.t("general:Type - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.type}
              onChange={(e) => {
                this.updateCaaseField("type", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Patient"),
              i18next.t("med:Patient - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.caase.patientName}> {
                this.updateCaaseField("patientName", value);
              }}
              options={this.state.patients.map((patient) => ({
                label: patient.name,
                value: patient.name,
              }))}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Doctor"),
              i18next.t("med:Doctor - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.caase.doctorName}> {
                this.updateCaaseField("doctorName", value);
              }}
              options={this.state.doctors.map((doctor) => ({
                label: doctor.name,
                value: doctor.name,
              }))}
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
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.caase.hospitalName}> {
                this.updateCaaseField("hospitalName", value);
              }}
              options={this.state.hospitals.map((hospital) => ({
                label: hospital.name,
                value: hospital.name,
              }))}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Specialist alliance Id"),
              i18next.t("med:Specialist alliance Id - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.specialistAllianceId}
              onChange={(e) => {
                this.updateCaaseField("specialistAllianceId", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(
              i18next.t("med:Integrated care organization Id"),
              i18next.t("med:Integrated care organization Id - Tooltip")
            )}{" "}
            :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.caase.integratedCareOrganizationId}
              onChange={(e) => {
                this.updateCaaseField(
                  "integratedCareOrganizationId",
                  e.target.value
                );
              }}
            />
          </div>
        </div>
      </div>
    );
  }

  submitCaaseEdit(willExist) {
    const caase = Setting.deepCopy(this.state.caase);
    CaaseBackend.updateCaase(this.state.caase.owner, this.state.caaseName, caase)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              caaseName: this.state.caase.name,
            });
            if (willExist) {
              this.props.history.push("/caases");
            } else {
              this.props.history.push(`/caases/${encodeURIComponent(this.state.caase.name)}`);
            }
            // this.getCaase(true);
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateCaaseField("name", this.state.caaseName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch((error) => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  deleteCaase() {
    CaaseBackend.deleteCaase(this.state.caase)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/caases");
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
        {this.state.caase !== null ? this.renderCaase() : null}
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitCaaseEdit(false)}>
            {i18next.t("general:Save")}
          </button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitCaaseEdit(true)}
          >
            {i18next.t("general:Save & Exit")}
          </button>
          {this.state.mode === "add" ? (
            <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteCaase()}
            >
              {i18next.t("general:Cancel")}
            </button>
          ) : null}
        </div>
      </div>
    );
  }
}

export default CaaseEditPage;
