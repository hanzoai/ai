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
import * as FormBackend from "./backend/FormBackend";
import i18next from "i18next";

const formTypeOptions = Setting.getFormTypeOptions();

class FormListPage extends BaseListPage {
  constructor(props) {
    super(props);
  }

  newForm() {
    const randomName = Setting.getRandomName();
    return {
      owner: this.props.account.owner,
      name: `form_${randomName}`,
      createdTime: moment().format(),
      displayName: `New Form - ${randomName}`,
      position: "1",
      category: "Table",
      url: "",
      formItems: [],
    };
  }

  addForm() {
    const newForm = this.newForm();
    FormBackend.addForm(newForm)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({
            pathname: `/forms/${newForm.name}`,
            state: {isNewForm: true},
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
    return FormBackend.deleteForm(this.state.data[i]);
  };

  deleteForm(record) {
    FormBackend.deleteForm(record)
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

  renderTable(forms) {
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
            <Link to={`/forms/${text}`}>
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
        title: i18next.t("form:Position"),
        dataIndex: "position",
        key: "position",
        width: "90px",
        sorter: (a, b) => a.position.localeCompare(b.position),
      },
      {
        title: i18next.t("general:Category"),
        dataIndex: "category",
        key: "category",
        width: "90px",
        sorter: (a, b) => a.category.localeCompare(b.category),
        ...this.getColumnSearchProps("category"),
      },
      {
        title: i18next.t("general:Type"),
        dataIndex: "type",
        key: "type",
        width: "120px",
        sorter: (a, b) => a.type.localeCompare(b.type),
        ...this.getColumnSearchProps("type"),
        render: (text, record, index) => {
          const typeOption = formTypeOptions.find(option => option.id === text);
          return typeOption ? i18next.t(typeOption.name) : text;
        },
      },
      {
        title: i18next.t("general:URL"),
        dataIndex: "url",
        key: "url",
        width: "220px",
        sorter: (a, b) => a.url.localeCompare(b.url),
        ...this.getColumnSearchProps("url"),
        render: (text, record, index) => {
          return (
            <a target="_blank" rel="noreferrer" href={text}>
              {text}
            </a>
          );
        },
      },
      {
        title: i18next.t("form:Form items"),
        dataIndex: "formItems",
        key: "formItems",
        ...this.getColumnSearchProps("formItems"),
        render: (text, record, index) => {
          const providers = text;
          if (!providers || providers.length === 0) {
            return `(${i18next.t("general:empty")})`;
          }

          const visibleProviders = providers.filter(item => item.visible !== false);
          const leftItems = [];
          const rightItems = [];
          visibleProviders.forEach((item, idx) => {
            if (idx % 2 === 0) {
              leftItems.push(item);
            } else {
              rightItems.push(item);
            }
          });

          const getList = (items) => (
            <List
              size="small"
              locale={{emptyText: " "}}
              dataSource={items}
              renderItem={providerItem => (
                <List.Item>
                  <div style={{display: "inline"}}>{i18next.t(providerItem.label)}</div>
                </List.Item>
              )}
            />
          );

          return (
            <div>
              <div className="flex flex-col sm:flex-row gap-2 mt-4">
                <div className="flex-1">{getList(leftItems)}</div>
                <div className="flex-1">{getList(rightItems)}</div>
              </div>
            </div>
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
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}> this.props.history.push(`/forms/${record.name}`)}>{i18next.t("general:Edit")}</button>
              this.deleteForm(record)}
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
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(forms || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
      </div>
    );
  }

  fetch = (params = {}) => {
    const field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    this.setState({loading: true});
    FormBackend.getForms(this.props.account.owner, params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
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

export default FormListPage;
