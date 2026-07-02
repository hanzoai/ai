// Copyright 2024 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2024 The OpenAgent Authors. All Rights Reserved.
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
import {Popconfirm, Tooltip} from "antd";
import {DeleteOutlined} from "@ant-design/icons";
import moment from "moment";
import BaseListPage from "./BaseListPage";
import * as Setting from "./Setting";
import * as ArticleBackend from "./backend/ArticleBackend";
import i18next from "i18next";

class ArticleListPage extends BaseListPage {
  constructor(props) {
    super(props);
  }

  newArticle() {
    const randomName = Setting.getRandomName();
    return {
      owner: this.props.account.name,
      name: `article_${randomName}`,
      createdTime: moment().format(),
      displayName: `New Article - ${randomName}`,
      provider: "provider_model_azure_gpt4",
      type: "",
      content: [],
      glossary: [],
    };
  }

  addArticle() {
    const newArticle = this.newArticle();
    ArticleBackend.addArticle(newArticle)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({
            pathname: `/articles/${newArticle.owner}/${newArticle.name}`,
            state: {isNewArticle: true},
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
    return ArticleBackend.deleteArticle(this.state.data[i]);
  };

  deleteArticle(record) {
    ArticleBackend.deleteArticle(record)
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

  renderTable(articles) {
    let columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "160px",
        sorter: (a, b) => a.name.localeCompare(b.name),
        ...this.getColumnSearchProps("name"),
        render: (text, record, index) => {
          return (
            <Link to={`/articles/${text}`}>
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
        title: i18next.t("store:Workflow"),
        dataIndex: "workflow",
        key: "workflow",
        width: "250px",
        sorter: (a, b) => a.workflow.localeCompare(b.workflow),
        ...this.getColumnSearchProps("workflow"),
        render: (text, record, index) => {
          return (
            <Link to={`/workflows/${text}`}>
              {text}
            </Link>
          );
        },
      },
      // {
      //   title: i18next.t("general:Type"),
      //   dataIndex: "type",
      //   key: "type",
      //   width: "90px",
      //   sorter: (a, b) => a.type.localeCompare(b.type),
      // },
      {
        title: i18next.t("article:Content"),
        dataIndex: "content",
        key: "content",
        // width: "160px",
        sorter: (a, b) => a.content.localeCompare(b.content),
        render: (text, record, index) => {
          if (!text) {
            return null;
          }

          const jsonText = JSON.stringify(text);
          return (
            <Tooltip placement="left" title={Setting.getShortText(jsonText, 1000)}>
              <div style={{maxWidth: "300px"}}>
                {Setting.getShortText(jsonText, 100)}
              </div>
            </Tooltip>
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
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}} onClick={() => this.props.history.push(`/articles/${record.name}`)}>{i18next.t("general:Edit")}</button>
              <Popconfirm
                title={`${i18next.t("general:Sure to delete")}: ${record.name} ?`}
                onConfirm={() => this.deleteArticle(record)}
                okText={i18next.t("general:OK")}
                cancelText={i18next.t("general:Cancel")}
              >
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-red-600 text-white hover:bg-red-700" style={{marginBottom: "10px"}}>{i18next.t("general:Delete")}</button>
              </Popconfirm>
            </div>
          );
        },
      },
    ];
    columns = Setting.filterTableColumns(columns, this.props.formItems ?? this.state.formItems);
    if (!this.props.account || this.props.account.name !== "admin") {
      columns = columns.filter(column => column.key !== "provider");
    }

    return (
      <div>
        <div className="flex flex-wrap items-center gap-2 mb-3">
          <span className="font-medium">{i18next.t("general:Articles")}</span>
          <button className="px-2 py-1 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" onClick={this.addArticle.bind(this)}>{i18next.t("general:Add")}</button>
          {this.state.selectedRowKeys.length > 0 && (
            <Popconfirm title={`${i18next.t("general:Sure to delete")}: ${this.state.selectedRowKeys.length} ${i18next.t("general:items")} ?`} onConfirm={() => this.performBulkDelete(this.state.selectedRows, this.state.selectedRowKeys)} okText={i18next.t("general:OK")} cancelText={i18next.t("general:Cancel")}>
              <button className="px-2 py-1 rounded text-xs font-medium transition-colors bg-red-600 text-white hover:bg-red-700 inline-flex items-center gap-1"><DeleteOutlined />{i18next.t("general:Delete")} ({this.state.selectedRowKeys.length})</button>
            </Popconfirm>
          )}
        </div>
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(articles || []).map((record, index) => <tr key={record.name || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
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
    ArticleBackend.getArticles(this.props.account.name, params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
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

export default ArticleListPage;
