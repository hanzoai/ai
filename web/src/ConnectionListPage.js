// Copyright 2024 Hanzo AI Inc. All Rights Reserved.
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

import * as ConnectionBackend from "./backend/ConnectionBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import PopconfirmModal from "./modal/PopconfirmModal";
import BaseListPage from "./BaseListPage";
import moment from "moment";
import React from "react";
import {Link} from "react-router-dom";

export const Connected = "connected";
const Disconnected = "disconnected";

class ConnectionListPage extends BaseListPage {
  constructor(props) {
    super(props);
    this.state = {
      ...this.state,
      status: Connected,
    };
  }

  componentDidUpdate(prevProps, prevState, snapshot) {
    if (this.state.status !== prevState.status) {
      this.fetch({
        pagination: this.state.pagination,
      });
    }
  }

  deleteItem = async(i) => {
    return ConnectionBackend.deleteConnection(this.state.data[i]);
  };

  deleteConnection(i) {
    ConnectionBackend.deleteConnection(this.state.data[i])
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
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  stopConnection(i) {
    ConnectionBackend.disconnect(Setting.GetIdFromObject(this.state.data[i]))
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully stopped"));
          this.setState({
            data: Setting.deleteRow(this.state.data, i),
            pagination: {
              ...this.state.pagination,
              total: this.state.pagination.total - 1,
            },
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to stop")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  renderTable(connections) {
    const columns = [
      {
        title: i18next.t("general:Node"),
        dataIndex: "node",
        key: "node",
        width: "200px",
        sorter: (a, b) => a.name.localeCompare(b.name),
        render: (text, record, index) => {
          return (
            <Link to={`/nodes/${text}`}>
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("machine:Protocol"),
        dataIndex: "protocol",
        key: "protocol",
        width: "120px",
        filterMultiple: false,
        filters: [
          {text: "RDP", value: "RDP"},
          {text: "VNC", value: "VNC"},
          {text: "SSH", value: "SSH"},
        ],
      },
      {
        title: i18next.t("general:User"),
        dataIndex: "creator",
        key: "creator",
        width: "150px",
        sorter: (a, b) => a.creator.localeCompare(b.creator),
        ...this.getColumnSearchProps("creator"),
        render: (text, record, index) => {
          return (
            <a target="_blank" rel="noreferrer" href={Setting.getUserProfileUrl(text, this.props.account)}>
              {text}
            </a>
          );
        },
      },
      {
        title: i18next.t("general:Client IP"),
        dataIndex: "clientIp",
        key: "clientIp",
        width: "150px",
        sorter: (a, b) => a.clientIp.localeCompare(b.clientIp),
        render: (text, record, index) => {
          if (text === "") {
            return null;
          }

          return (
            <a target="_blank" rel="noreferrer" href={`https://db-ip.com/${text}`}>
              {
                record.clientIpDesc === "" ? text : (
                  <div>
                    {text}
                    <br />
                    {record.clientIpDesc}
                  </div>
                )
              }
            </a>
          );
        },
      },
      {
        title: i18next.t("general:User agent"),
        dataIndex: "userAgent",
        key: "userAgent",
        width: "150px",
        ...this.getColumnSearchProps("userAgent"),
        sorter: (a, b) => a.userAgent.localeCompare(b.userAgent),
        render: (text, record, index) => {
          if (!record.userAgentDesc) {
            return text;
          } else {
            return record.userAgentDesc.split("|").map(text => {
              if (text.includes("Other") || text.includes("Generic Smartphone")) {
                return null;
              }

              return (
                <div key={text}>{text}</div>
              );
            });
          }
        },
      },
      {
        title: i18next.t("video:Start time (s)"),
        dataIndex: "startTime",
        key: "startTime",
        width: "220px",
        sorter: (a, b) => a.startTime.localeCompare(b.startTime),
        render: (text, node, index) => {
          return Setting.getFormattedDate(text);
        },
      },
      {
        title: i18next.t("video:Time"),
        dataIndex: "startTimeDur",
        key: "startTimeDur",
        // width: "200px",
        render: (text, record) => {
          if (!record["startTime"]) {
            return "-";
          }
          const startTime = moment(record["startTime"]);
          const currentTime = moment();
          const duration = moment.duration(currentTime.diff(startTime));
          return `${duration.hours()}h ${duration.minutes()}m ${duration.seconds()}s`;
        },
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "120px",
        fixed: (Setting.isMobile()) ? "false" : "right",
        render: (text, record, index) => {
          return this.state.status === Connected ?
            (
              <div>
                <PopconfirmModal
                  style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}}
                  text={i18next.t("general:Stop")}
                  title={`${i18next.t("general:Sure to disconnect from")}: ${record.name} ?`}
                  onConfirm={() => this.stopConnection(index)}
                />
              </div>
            ) : (
              <PopconfirmModal
                style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}}
                title={`${i18next.t("general:Sure to delete")}: ${record.name} ?`}
                onConfirm={() => this.deleteConnection(index)}
              />
            );
        },
      },
    ];

    const paginationProps = {
      pageSize: this.state.pagination.pageSize,
      total: this.state.pagination.total,
      showQuickJumper: true,
      showSizeChanger: true,
      pageSizeOptions: ["10", "20", "50", "100", "1000", "10000", "100000"],
      showTotal: () => i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total),
    };

    return (
      <div>
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(connections || []).map((record, index) => <tr key={typeof {(record) => `${record.owner} === "function" ? ({(record) => `${record.owner})(record) : record[{(record) => `${record.owner}] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
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

    this.setState({
      loading: true,
    });

    ConnectionBackend.getConnections(Setting.getRequestOrganization(this.props.account), params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder, this.state.status).then((res) => {
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

export default ConnectionListPage;
