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
import {Button, Popconfirm, Switch, Table, Tag} from "antd";
import moment from "moment";
import BaseListPage from "./BaseListPage";
import * as Setting from "./Setting";
import * as ModelRouteBackend from "./backend/ModelRouteBackend";
import i18next from "i18next";
import {DeleteOutlined} from "@ant-design/icons";

class ModelRouteListPage extends BaseListPage {
  constructor(props) {
    super(props);
  }

  newModelRoute() {
    return {
      owner: "admin",
      modelName: "",
      createdTime: moment().format(),
      updatedTime: moment().format(),
      provider: "",
      upstream: "",
      fallback1Provider: "",
      fallback1Upstream: "",
      fallback2Provider: "",
      fallback2Upstream: "",
      ownedBy: "",
      premium: false,
      hidden: false,
      inputPricePerMillion: 0,
      outputPricePerMillion: 0,
      enabled: true,
    };
  }

  addModelRoute() {
    const newRoute = this.newModelRoute();
    ModelRouteBackend.addModelRoute(newRoute)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.setState({
            data: [newRoute, ...(this.state.data || [])],
            pagination: {
              ...this.state.pagination,
              total: (this.state.pagination?.total || 0) + 1,
            },
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
    return ModelRouteBackend.deleteModelRoute(this.state.data[i]);
  };

  deleteModelRoute(record) {
    ModelRouteBackend.deleteModelRoute(record)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deleted"));
          this.setState({
            data: this.state.data.filter((item) => item.modelName !== record.modelName),
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

  renderTable(routes) {
    const columns = [
      {
        title: i18next.t("general:Model Name"),
        dataIndex: "modelName",
        key: "modelName",
        width: "200px",
        sorter: (a, b) => a.modelName.localeCompare(b.modelName),
        ...this.getColumnSearchProps("modelName"),
        render: (text, record, index) => {
          return (
            <Link to={`/model-routes/${record.owner}/${encodeURIComponent(text)}`}>
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("general:Owner"),
        dataIndex: "owner",
        key: "owner",
        width: "120px",
        sorter: (a, b) => a.owner.localeCompare(b.owner),
      },
      {
        title: i18next.t("general:Provider"),
        dataIndex: "provider",
        key: "provider",
        width: "150px",
        sorter: (a, b) => a.provider.localeCompare(b.provider),
        ...this.getColumnSearchProps("provider"),
      },
      {
        title: "Upstream",
        dataIndex: "upstream",
        key: "upstream",
        width: "250px",
        sorter: (a, b) => a.upstream.localeCompare(b.upstream),
        ...this.getColumnSearchProps("upstream"),
        render: (text) => {
          return (
            <span style={{fontFamily: "monospace", fontSize: "12px"}}>{Setting.getShortText(text, 60)}</span>
          );
        },
      },
      {
        title: "Fallback 1",
        dataIndex: "fallback1Provider",
        key: "fallback1Provider",
        width: "120px",
      },
      {
        title: "Premium",
        dataIndex: "premium",
        key: "premium",
        width: "90px",
        sorter: (a, b) => a.premium - b.premium,
        render: (text) => {
          return text ? <Tag color="gold">Premium</Tag> : <Tag>Free</Tag>;
        },
      },
      {
        title: "Hidden",
        dataIndex: "hidden",
        key: "hidden",
        width: "80px",
        render: (text) => {
          return (
            <Switch disabled checkedChildren={i18next.t("general:ON")} unCheckedChildren={i18next.t("general:OFF")} checked={text} />
          );
        },
      },
      {
        title: "Input $/M",
        dataIndex: "inputPricePerMillion",
        key: "inputPricePerMillion",
        width: "100px",
        sorter: (a, b) => a.inputPricePerMillion - b.inputPricePerMillion,
        render: (text) => text > 0 ? `$${text.toFixed(2)}` : "-",
      },
      {
        title: "Output $/M",
        dataIndex: "outputPricePerMillion",
        key: "outputPricePerMillion",
        width: "100px",
        sorter: (a, b) => a.outputPricePerMillion - b.outputPricePerMillion,
        render: (text) => text > 0 ? `$${text.toFixed(2)}` : "-",
      },
      {
        title: i18next.t("general:Enabled"),
        dataIndex: "enabled",
        key: "enabled",
        width: "90px",
        sorter: (a, b) => a.enabled - b.enabled,
        render: (text) => {
          return (
            <Switch disabled checkedChildren={i18next.t("general:ON")} unCheckedChildren={i18next.t("general:OFF")} checked={text} />
          );
        },
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "180px",
        fixed: "right",
        render: (text, record, index) => {
          return (
            <div>
              <Button
                style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}}
                type="primary"
                onClick={() => this.props.history.push(`/model-routes/${record.owner}/${encodeURIComponent(record.modelName)}`)}
              >
                {i18next.t("general:Edit")}
              </Button>
              <Popconfirm
                title={`${i18next.t("general:Sure to delete")}: ${record.modelName} ?`}
                onConfirm={() => this.deleteModelRoute(record)}
                okText={i18next.t("general:OK")}
                cancelText={i18next.t("general:Cancel")}
              >
                <Button
                  style={{marginBottom: "10px"}}
                  type="primary"
                  danger
                >
                  {i18next.t("general:Delete")}
                </Button>
              </Popconfirm>
            </div>
          );
        },
      },
    ];

    const paginationProps = {
      total: this.state.pagination.total,
      showQuickJumper: true,
      showSizeChanger: true,
      pageSizeOptions: ["10", "20", "50", "100", "1000"],
      showTotal: () => i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total),
    };

    return (
      <div>
        <Table scroll={{x: "max-content"}} columns={columns} dataSource={routes} rowKey={(record) => `${record.owner}/${record.modelName}`} rowSelection={this.getRowSelection()} size="middle" bordered pagination={paginationProps}
          title={() => (
            <div>
              {"Model Routes"}&nbsp;&nbsp;&nbsp;&nbsp;
              <Button type="primary" size="small" onClick={() => this.props.history.push("/model-routes/new")}>{i18next.t("general:Add")}</Button>
              {this.state.selectedRowKeys && this.state.selectedRowKeys.length > 0 && (
                <Popconfirm title={`${i18next.t("general:Sure to delete")}: ${this.state.selectedRowKeys.length} ${i18next.t("general:items")} ?`} onConfirm={() => this.performBulkDelete(this.state.selectedRows, this.state.selectedRowKeys)} okText={i18next.t("general:OK")} cancelText={i18next.t("general:Cancel")}>
                  <Button type="primary" danger size="small" icon={<DeleteOutlined />} style={{marginLeft: 8}}>
                    {i18next.t("general:Delete")} ({this.state.selectedRowKeys.length})
                  </Button>
                </Popconfirm>
              )}
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
    ModelRouteBackend.getModelRoutes("admin", params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
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

export default ModelRouteListPage;
