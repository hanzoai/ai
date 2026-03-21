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
// import * as ConsultationBackend from "./backend/ConsultationBackend";
import * as PatientBackend from "./backend/PatientBackend";
import * as DoctorBackend from "./backend/DoctorBackend";
import * as HospitalBackend from "./backend/HospitalBackend";
import * as Setting from "./Setting";
import i18next from "i18next";

class ConsultationEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,

      consultationName: props.match.params.consultationName,
      consultation: null,
      patients: [],
      doctors: [],
      hospitals: [],
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getConsultation();
    this.getPatients();
    this.getDoctors();
    this.getHospitals();
  }

  getConsultation() {
    ConsultationBackend.getConsultation(this.props.account.owner, this.state.consultationName)
      .then((res) => {
        if (res.status === "ok") {
          const consultation = res.data;
          // Ensure doctorNames is an array
          if (!consultation.doctorNames) {
            consultation.doctorNames = [];
          }
          this.setState({
            consultation: consultation,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
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

  parseConsultationField(key, value) {
    if ([].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateConsultationField(key, value) {
    value = this.parseConsultationField(key, value);

    const consultation = this.state.consultation;
    consultation[key] = value;
    this.setState({
      consultation: consultation,
    });
  }

  renderConsultation() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {this.state.mode === "add" ? i18next.t("consultation:New Consultation") : i18next.t("consultation:Edit Consultation")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitConsultationEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitConsultationEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteConsultation()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.consultation.name} onChange={e => {
              this.updateConsultationField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("med:Patient"), i18next.t("med:Patient - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.consultation.patientName}> {
                this.updateConsultationField("patientName", value);
              }}
              options={this.state.patients.map((patient) => ({
                label: patient.hospitalName ? `${patient.hospitalName}/${patient.name}` : patient.name,
                value: patient.name,
              }))}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("med:Doctors"), i18next.t("med:Doctors - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.consultation.doctorNames || []}> {
                this.updateConsultationField("doctorNames", value);
              }}
              options={this.state.doctors.map((doctor) => ({
                label: doctor.hospitalName ? `${doctor.hospitalName}/${doctor.name}` : doctor.name,
                value: doctor.name,
              }))}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("med:Expired time"), i18next.t("med:Expired time - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.consultation.expiredTime} onChange={e => {
              this.updateConsultationField("expiredTime", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:State"), i18next.t("general:State - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.consultation.state}> {
              this.updateConsultationField("state", value);
            }}
            options={[
              {value: "Active", label: "Active"},
              {value: "Inactive", label: "Inactive"},
            ].map(item => Setting.getOption(item.label, item.value))} />
          </div>
        </div>
      </div>
    );
  }

  submitConsultationEdit(willExist) {
    const consultation = Setting.deepCopy(this.state.consultation);
    ConsultationBackend.updateConsultation(this.state.consultation.owner, this.state.consultationName, consultation)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              consultationName: this.state.consultation.name,
            });
            if (willExist) {
              this.props.history.push("/consultations");
            } else {
              this.props.history.push(`/consultations/${encodeURIComponent(this.state.consultation.name)}`);
            }
            // this.getConsultation(true);
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateConsultationField("name", this.state.consultationName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  deleteConsultation() {
    ConsultationBackend.deleteConsultation(this.state.consultation)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/consultations");
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
          this.state.consultation !== null ? this.renderConsultation() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitConsultationEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitConsultationEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteConsultation()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      </div>
    );
  }
}

export default ConsultationEditPage;
