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
import {Link} from "react-router-dom";
import BaseListPage from "./BaseListPage";
import moment from "moment";
import * as Setting from "./Setting";
import * as HospitalBackend from "./backend/HospitalBackend";
import i18next from "i18next";
import PopconfirmModal from "./modal/PopconfirmModal";

class HospitalListPage extends BaseListPage {
  constructor(props) {
    super(props);
  }

  newHospital() {
    return {
      owner: this.props.account.owner,
      name: `hospital_${Setting.getRandomName()}`,
      createdTime: moment().format(),
      updatedTime: moment().format(),
      displayName: `New Hospital - ${Setting.getRandomName()}`,
      address: "",
    };
  }

  addHospital() {
    const newHospital = this.newHospital();
    HospitalBackend.addHospital(newHospital)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push({pathname: `/hospitals/${newHospital.name}`, mode: "add"});
          Setting.showMessage("success", i18next.t("general:Successfully added"));
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  deleteHospital(i) {
    HospitalBackend.deleteHospital(this.state.data[i])
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deleted"));
          this.setState({
            data: Setting.deleteRow(this.state.data, i),
            pagination: {
              ...this.state.pagination,
              total: this.state.pagination.total - 1,
            },
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${error}`);
      });
  }

  renderTable(hospitals) {
    const columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "300px",
        sorter: true,
        ...this.getColumnSearchProps("name"),
        render: (text, record, index) => {
          return (
            <Link to={`/hospitals/${record.name}`}>{text}</Link>
          );
        },
      },
      {
        title: i18next.t("general:Created time"),
        dataIndex: "createdTime",
        key: "createdTime",
        width: "200px",
        // sorter: true,
        sorter: (a, b) => a.createdTime.localeCompare(b.createdTime),
        render: (text, hospital, index) => {
          return Setting.getFormattedDate(text);
        },
      },
      {
        title: i18next.t("med:Address"),
        dataIndex: "address",
        key: "address",
        // width: "90px",
        sorter: (a, b) => a.address.localeCompare(b.address),
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "220px",
        fixed: (Setting.isMobile()) ? "false" : "right",
        render: (text, hospital, index) => {
          return (
            <div>
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}> this.props.history.push(`/hospitals/${hospital.name}`)}
              >{i18next.t("general:Edit")}
              </button>
              <PopconfirmModal
                disabled={hospital.owner !== this.props.account.owner}
                style={{marginBottom: "10px"}}
                title={i18next.t("general:Sure to delete") + `: ${hospital.name} ?`}
                onConfirm={() => this.deleteHospital(index)}
              >
              </PopconfirmModal>
            </div>
          );
        },
      },
    ];

    const paginationProps = {
      pageSize: this.state.pagination.pageSize,
      total: this.state.pagination.total,
      showQuickJumper: true,
      showSizeChanger: true,
      showTotal: () => i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total),
    };

    return (
      <div>
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(hospitals || []).map((record, index) => <tr key={typeof {(hospital) => `${hospital.owner} === "function" ? ({(hospital) => `${hospital.owner})(record) : record[{(hospital) => `${hospital.owner}] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
      </div>
    );
  }

  fetch = (params = {}) => {
    let field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    if (params.type !== undefined && params.type !== null) {
      field = "type";
      value = params.type;
    }
    this.setState({loading: true});
    HospitalBackend.getHospitals(Setting.getRequestOrganization(this.props.account), params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
      .then((res) => {
        this.setState({
          loading: false,
        });
        if (res.status === "ok") {
          this.setState({
            data: res.data,
            pagination: {
              ...params.pagination,
              total: res.data2,
            },
            searchText: params.searchText,
            searchedColumn: params.searchedColumn,
          });
        } else {
          if (Setting.isResponseDenied(res)) {
            this.setState({
              isAuthorized: false,
            });
          } else {
            Setting.showMessage("error", res.msg);
          }
        }
      });
  };
}

export default HospitalListPage;
