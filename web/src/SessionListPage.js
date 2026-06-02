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

import BaseListPage from "./BaseListPage";
import * as Setting from "./Setting";
import i18next from "i18next";
import React from "react";
import * as SessionBackend from "./backend/SessionBackend";
import PopconfirmModal from "./modal/PopconfirmModal";

class SessionListPage extends BaseListPage {
  deleteItem = async(i) => {
    return SessionBackend.deleteSession(this.state.data[i]);
  };

  deleteSession(i) {
    SessionBackend.deleteSession(this.state.data[i])
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

  renderTable(sessions) {
    const columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "150px",
        fixed: "left",
        sorter: (a, b) => a.name.localeCompare(b.name),
        ...this.getColumnSearchProps("name"),
        render: (text, session, index) => {
          return (
            <a target="_blank" rel="noreferrer" href={Setting.getMyProfileUrl(this.props.account).replace("/account", `/users/${session.owner}/${session.name}`)}>
              {text}
            </a>
          );
        },
      },
      {
        title: i18next.t("general:Organization"),
        dataIndex: "owner",
        key: "owner",
        width: "110px",
        sorter: (a, b) => a.owner.localeCompare(b.owner),
        ...this.getColumnSearchProps("owner"),
        render: (text, session, index) => {
          return (
            <a target="_blank" rel="noreferrer" href={Setting.getMyProfileUrl(this.props.account).replace("/account", `/organizations/${text}`)}>
              {text}
            </a>
          );
        },
      },
      {
        title: i18next.t("general:Created time"),
        dataIndex: "createdTime",
        key: "createdTime",
        width: "180px",
        sorter: (a, b) => a.createdTime.localeCompare(b.createdTime),
        render: (text, session, index) => {
          return Setting.getFormattedDate(text);
        },
      },
      {
        title: i18next.t("general:ID"),
        dataIndex: "sessionId",
        key: "sessionId",
        width: "180px",
        sorter: (a, b) => a.sessionId.localeCompare(b.sessionId),
        render: (text, session, index) => {
          return text.map((item, index) =>
            <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">{item}</span>
          );
        },
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "120px",
        fixed: (Setting.isMobile()) ? "false" : "right",
        render: (text, session, index) => {
          return (
            <div>
              <PopconfirmModal
                style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}}
                title={`${i18next.t("general:Sure to delete")}: ${session.name} ?`}
                onConfirm={() => this.deleteSession(index)}
              />
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
      pageSizeOptions: ["10", "20", "50", "100", "1000", "10000", "100000"],
      showTotal: () => i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total),
    };

    return (
      <div>
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(sessions || []).map((record, index) => <tr key={typeof {(session) => `${session.owner} === "function" ? ({(session) => `${session.owner})(record) : record[{(session) => `${session.owner}] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
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

    SessionBackend.getSessions(Setting.getRequestOrganization(this.props.account), params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder).then((res) => {
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

export default SessionListPage;
