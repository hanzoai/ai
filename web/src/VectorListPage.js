// Copyright 2023 Hanzo AI Inc. All Rights Reserved.
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
/* eslint-disable unused-imports/no-unused-imports */
import {Table} from "antd";
/* eslint-enable unused-imports/no-unused-imports */
import moment from "moment";
import BaseListPage from "./BaseListPage";
import * as Setting from "./Setting";
import * as VectorBackend from "./backend/VectorBackend";
import i18next from "i18next";
import {Trash2} from "lucide-react";

class VectorListPage extends BaseListPage {
  constructor(props) {
    super(props);
  }

  newVector() {
    const randomName = Setting.getRandomName();
    const storeName = Setting.isDefaultStoreSelected(this.props.account) ? "store-built-in" : Setting.getRequestStore(this.props.account);
    return {
      owner: "admin",
      name: `vector_${randomName}`,
      createdTime: moment().format(),
      displayName: `New Vector - ${randomName}`,
      store: storeName,
      file: "/aaa/example.txt",
      text: "The text of vector",
      data: [0.1, 0.2, 0.3],
    };
  }

  addVector() {
    const newVector = this.newVector();
    VectorBackend.addVector(newVector)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({
            pathname: `/vectors/${newVector.name}`,
            state: {isNewVector: true},
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
    return VectorBackend.deleteVector(this.state.data[i]);
  };

  deleteVector(record) {
    VectorBackend.deleteVector(record)
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

  deleteAllVectors() {
    VectorBackend.deleteAllVectors()
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deleted"));
          this.setState({
            data: [],
            pagination: {
              ...this.state.pagination,
              current: 1,
              total: 0,
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

  renderTable(vectors) {
    const columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "140px",
        sorter: (a, b) => a.name.localeCompare(b.name),
        ...this.getColumnSearchProps("name"),
        render: (text) => <Link to={`/vectors/${text}`} className="text-primary hover:underline">{text}</Link>,
      },
      {
        title: i18next.t("general:Store"),
        dataIndex: "store",
        key: "store",
        width: "130px",
        sorter: (a, b) => a.store.localeCompare(b.store),
        ...this.getColumnSearchProps("store"),
        render: (text, record) => <Link to={`/stores/${record.owner}/${text}`} className="text-primary hover:underline">{text}</Link>,
      },
      {
        title: i18next.t("general:Provider"),
        dataIndex: "provider",
        key: "provider",
        width: "200px",
        sorter: (a, b) => a.provider.localeCompare(b.provider),
        ...this.getColumnSearchProps("provider"),
        render: (text) => <Link to={`/providers/${text}`} className="text-primary hover:underline">{text}</Link>,
      },
      {
        title: i18next.t("store:File"),
        dataIndex: "file",
        key: "file",
        width: "200px",
        sorter: (a, b) => a.file.localeCompare(b.file),
        ...this.getColumnSearchProps("file"),
      },
      {
        title: i18next.t("vector:Index"),
        dataIndex: "index",
        key: "index",
        width: "80px",
        sorter: (a, b) => a.index - b.index,
      },
      {
        title: i18next.t("general:Text"),
        dataIndex: "text",
        key: "text",
        width: "200px",
        sorter: (a, b) => a.text.localeCompare(b.text),
        ...this.getColumnSearchProps("text"),
        render: (text) => (
          <div className="max-w-[200px] truncate text-sm" title={text}>
            {Setting.getShortText(text, 60)}
          </div>
        ),
      },
      {
        title: i18next.t("general:Size"),
        dataIndex: "size",
        key: "size",
        width: "80px",
        sorter: (a, b) => a.size - b.size,
      },
      {
        title: i18next.t("general:Data"),
        dataIndex: "data",
        key: "data",
        width: "200px",
        sorter: (a, b) => a.data.localeCompare(b.data),
        render: (text) => (
          <div className="max-w-[200px] truncate text-sm" title={Setting.getShortText(JSON.stringify(text), 1000)}>
            {Setting.getShortText(JSON.stringify(text), 50)}
          </div>
        ),
      },
      {
        title: i18next.t("vector:Dimension"),
        dataIndex: "dimension",
        key: "dimension",
        width: "80px",
        sorter: (a, b) => a.dimension - b.dimension,
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "150px",
        fixed: "right",
        render: (text, record) => (
          <div className="flex flex-col gap-2">
            <button onClick={() => this.props.history.push(`/vectors/${record.name}`)} className="rounded bg-primary px-3 py-1 text-xs text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Edit")}</button>
            <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${record.name} ?`)) { this.deleteVector(record); } }} className="rounded bg-destructive px-3 py-1 text-xs text-destructive-foreground hover:bg-destructive/90 transition-colors">{i18next.t("general:Delete")}</button>
          </div>
        ),
      },
    ];
    const filteredColumns = Setting.filterTableColumns(columns, this.props.formItems ?? this.state.formItems);
    const paginationProps = {
      total: this.state.pagination.total,
      showQuickJumper: true,
      showSizeChanger: true,
      pageSizeOptions: ["10", "20", "50", "100", "1000", "10000", "100000"],
      showTotal: () => i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total),
    };

    return (
      <div>
        <Table scroll={{x: "max-content"}} columns={filteredColumns} dataSource={vectors} rowKey="name" rowSelection={this.getRowSelection()} size="middle" bordered pagination={paginationProps}
          title={() => (
            <div className="flex items-center gap-3 flex-wrap">
              <span className="text-foreground font-medium">{i18next.t("general:Vectors")}</span>
              <button onClick={this.addVector.bind(this)} className="rounded bg-primary px-3 py-1 text-xs text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Add")}</button>
              {this.state.selectedRowKeys.length > 0 && (
                <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${this.state.selectedRowKeys.length} ${i18next.t("general:items")} ?`)) { this.performBulkDelete(this.state.selectedRows, this.state.selectedRowKeys); } }} className="inline-flex items-center gap-1 rounded bg-destructive px-3 py-1 text-xs text-destructive-foreground hover:bg-destructive/90 transition-colors">
                  <Trash2 className="w-3 h-3" />
                  {i18next.t("general:Delete")} ({this.state.selectedRowKeys.length})
                </button>
              )}
              <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete all")} ${i18next.t("general:Vectors")}?`)) { this.deleteAllVectors(); } }} className="rounded bg-destructive px-3 py-1 text-xs text-destructive-foreground hover:bg-destructive/90 transition-colors">{i18next.t("general:Delete All")}</button>
            </div>
          )}
          loading={this.state.loading}
          onChange={this.handleTableChange}
        />
      </div>
    );
  }

  fetch = (params = {}) => {
    const field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    this.setState({loading: true});
    VectorBackend.getVectors("admin", Setting.getRequestStore(this.props.account), params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
      .then((res) => {
        this.setState({loading: false});
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
            this.setState({isAuthorized: false});
          } else {
            Setting.showMessage("error", res.msg);
          }
        }
      });
  };
}

export default VectorListPage;
