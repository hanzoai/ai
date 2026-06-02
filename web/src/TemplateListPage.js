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
import moment from "moment";
import BaseListPage from "./BaseListPage";
import * as Setting from "./Setting";
import * as TemplateBackend from "./backend/TemplateBackend";
import i18next from "i18next";

class TemplateListPage extends BaseListPage {
  constructor(props) {
    super(props);
  }

  newTemplate() {
    const randomName = Setting.getRandomName();
    const defaultManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-deployment
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx
        ports:
        - containerPort: 80`;

    return {
      owner: this.props.account.name,
      name: `template_${randomName}`,
      createdTime: moment().format(),
      displayName: `${i18next.t("template:New Template")} - ${randomName}`,
      description: "",
      version: "1.0.0",
      icon: Setting.getDefaultAiAvatar(),
      manifest: defaultManifest,
    };
  }

  addTemplate() {
    const newTemplate = this.newTemplate();
    TemplateBackend.addTemplate(newTemplate)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({
            pathname: `/templates/${newTemplate.name}`,
            state: {isNewTemplate: true},
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  deleteItem = async(i) => {
    return TemplateBackend.deleteTemplate(this.state.data[i]);
  };

  deleteTemplate(record) {
    TemplateBackend.deleteTemplate(record)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deleted"));
          this.setState({
            data: this.state.data.filter((item) => item.name !== record.name),
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

  renderTable(templates) {
    const columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "160px",
        sorter: (a, b) => a.name.localeCompare(b.name),
        ...this.getColumnSearchProps("name"),
        render: (text, record, index) => {
          return (
            <Link to={`/templates/${text}`}>
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("general:Display name"),
        dataIndex: "displayName",
        key: "displayName",
        width: "200px",
        sorter: (a, b) => a.displayName.localeCompare(b.displayName),
        ...this.getColumnSearchProps("displayName"),
      },
      {
        title: i18next.t("general:Created time"),
        dataIndex: "createdTime",
        key: "createdTime",
        width: "160px",
        sorter: (a, b) => a.createdTime.localeCompare(b.createdTime),
        render: (text, record, index) => {
          return Setting.getFormattedDate(text);
        },
      },
      {
        title: i18next.t("general:Description"),
        dataIndex: "description",
        key: "description",
        width: "300px",
        sorter: (a, b) => a.description.localeCompare(b.description),
        ...this.getColumnSearchProps("description"),
        render: (text, record, index) => {
          return (
            
              <div style={{maxWidth: "300px"}}>
                {Setting.getShortText(text, 100)}
              </div>
            
          );
        },
      },
      {
        title: i18next.t("general:Version"),
        dataIndex: "version",
        key: "version",
        width: "100px",
        sorter: (a, b) => a.version.localeCompare(b.version),
        ...this.getColumnSearchProps("version"),
      },
      {
        title: i18next.t("general:Icon"),
        dataIndex: "icon",
        key: "icon",
        align: "center",
        width: "100px",
        sorter: (a, b) => a.icon.localeCompare(b.icon),
        render: (text, record, index) => {
          if (text === "") {
            return null;
          }
          return (
            <img src={text} alt={text} style={{maxWidth: "50px", maxHeight: "50px"}} />
          );
        },
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "180px",
        fixed: (Setting.isMobile()) ? "false" : "right",
        render: (text, record, index) => {
          return (
            <div>
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}> this.props.history.push(`/templates/${record.name}`)}>{i18next.t("general:Edit")}</button>
              this.deleteTemplate(record)}
                okText={i18next.t("general:OK")}
                cancelText={i18next.t("general:Cancel")}
              >
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-red-600 text-white hover:bg-red-700" style={{marginBottom: "10px"}>{i18next.t("general:Delete")}</button>
            </div>
          );
        },
      },
    ];

    const paginationProps = {
      total: this.state.pagination.total,
      showQuickJumper: true,
      showSizeChanger: true,
      pageSizeOptions: ["10", "20", "50", "100", "1000", "10000", "100000"],
      showTotal: () => i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total),
    };

    return (
      <div>
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(templates || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
      </div>
    );
  }

  fetch = (params = {}) => {
    const field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    this.setState({loading: true});
    TemplateBackend.getTemplates(this.props.account.name, params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
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

export default TemplateListPage;
