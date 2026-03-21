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
import * as FileBackend from "./backend/FileBackend";
import * as StoreBackend from "./backend/StoreBackend";
import i18next from "i18next";
import {Trash2, Loader2} from "lucide-react";

class FileListPage extends BaseListPage {
  constructor(props) {
    super(props);
    this.state = {
      ...this.state,
      refreshing: {},
    };
  }

  newFile() {
    const randomName = Setting.getRandomName();
    const storeName = Setting.isDefaultStoreSelected(this.props.account) ? "store-built-in" : Setting.getRequestStore(this.props.account);
    const objectKey = `file/file_${randomName}.txt`;
    return {
      owner: "admin",
      name: `${storeName}_${objectKey}`,
      createdTime: moment().format(),
      filename: `file_${randomName}.txt`,
      size: 0,
      store: storeName,
      storageProvider: "",
      tokenCount: 0,
      status: "Pending",
      errorText: "",
    };
  }

  addFile = async() => {
    let storeName;
    if (Setting.isDefaultStoreSelected(this.props.account)) {
      try {
        const res = await StoreBackend.getStore("admin", "_cloud_default_store_");
        if (res.status === "ok" && res.data?.name) {
          storeName = res.data.name;
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
          return;
        }
      } catch (error) {
        Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${error}`);
        return;
      }
    } else {
      storeName = Setting.getRequestStore(this.props.account);
      if (!storeName) {
        Setting.showMessage("error", i18next.t("general:Store is not available"));
        return;
      }
    }
    this.props.history.push(`/stores/admin/${storeName}/view`);
  };

  deleteItem = async(i) => {
    return FileBackend.deleteFile(this.state.data[i]);
  };

  deleteFile(record) {
    FileBackend.deleteFile(record)
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

  refreshFileVectors(index) {
    this.setState(prevState => ({
      refreshing: {
        ...prevState.refreshing,
        [index]: true,
      },
    }));
    FileBackend.refreshFileVectors(this.state.data[index])
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Vectors generated successfully"));
          this.fetch({pagination: this.state.pagination});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Vectors failed to generate")}: ${res.msg}`);
        }
        this.setState(prevState => ({
          refreshing: {
            ...prevState.refreshing,
            [index]: false,
          },
        }));
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Vectors failed to generate")}: ${error}`);
        this.setState(prevState => ({
          refreshing: {
            ...prevState.refreshing,
            [index]: false,
          },
        }));
      });
  }

  renderTable(files) {
    const columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        sorter: (a, b) => a.name.localeCompare(b.name),
        ...this.getColumnSearchProps("name"),
        render: (text, record, index) => {
          return (
            <Link to={`/files/${encodeURIComponent(text)}`} className="text-blue-400 hover:text-blue-300">
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("file:Filename"),
        dataIndex: "filename",
        key: "filename",
        sorter: (a, b) => a.filename.localeCompare(b.filename),
        ...this.getColumnSearchProps("filename"),
      },
      {
        title: i18next.t("general:Size"),
        dataIndex: "size",
        key: "size",
        sorter: (a, b) => a.size - b.size,
        render: (text) => Setting.getFormattedSize(text),
      },
      {
        title: i18next.t("general:Store"),
        dataIndex: "store",
        key: "store",
        sorter: (a, b) => a.store.localeCompare(b.store),
        ...this.getColumnSearchProps("store"),
        render: (text, record) => (
          <Link to={`/stores/${record.owner}/${text}`} className="text-blue-400 hover:text-blue-300">
            {text}
          </Link>
        ),
      },
      {
        title: i18next.t("store:Storage provider"),
        dataIndex: "storageProvider",
        key: "storageProvider",
        sorter: (a, b) => a.storageProvider.localeCompare(b.storageProvider),
        ...this.getColumnSearchProps("storageProvider"),
        render: (text) => (
          <Link to={`/providers/${text}`} className="text-blue-400 hover:text-blue-300">
            {text}
          </Link>
        ),
      },
      {
        title: i18next.t("chat:Token count"),
        dataIndex: "tokenCount",
        key: "tokenCount",
        sorter: (a, b) => a.tokenCount - b.tokenCount,
      },
      {
        title: i18next.t("general:Status"),
        dataIndex: "status",
        key: "status",
        sorter: (a, b) => a.status.localeCompare(b.status),
        ...this.getColumnSearchProps("status"),
      },
      {
        title: i18next.t("message:Error text"),
        dataIndex: "errorText",
        key: "errorText",
        sorter: (a, b) => a.errorText.localeCompare(b.errorText),
        ...this.getColumnSearchProps("errorText"),
        render: (text) => (
          <div dangerouslySetInnerHTML={{__html: text}} />
        ),
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        render: (text, record, index) => {
          return (
            <div className="flex flex-wrap gap-2">
              {Setting.isLocalAdminUser(this.props.account) && (
                <button
                  disabled={this.state.refreshing[index]}
                  onClick={() => this.refreshFileVectors(index)}
                  className="px-3 py-1.5 bg-zinc-800 text-zinc-300 rounded text-xs hover:bg-zinc-700 transition-colors disabled:opacity-50 flex items-center gap-1"
                >
                  {this.state.refreshing[index] && <Loader2 className="w-3 h-3 animate-spin" />}
                  {i18next.t("general:Refresh Vectors")}
                </button>
              )}
              <button
                onClick={() => this.props.history.push(`/files/${encodeURIComponent(record.name)}`)}
                className="px-3 py-1.5 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors"
              >
                {i18next.t("general:View")}
              </button>
              <button
                onClick={() => {
                  if (window.confirm(`${i18next.t("general:Sure to delete")}: ${record.name} ?`)) {
                    this.deleteFile(record);
                  }
                }}
                className="px-3 py-1.5 bg-red-600 text-white rounded text-xs font-medium hover:bg-red-700 transition-colors"
              >
                {i18next.t("general:Delete")}
              </button>
            </div>
          );
        },
      },
    ];

    const filteredColumns = Setting.filterTableColumns(columns, this.props.formItems ?? this.state.formItems);

    return (
      <div>
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-3">
            <h2 className="text-lg font-semibold text-white">{i18next.t("general:Files")}</h2>
            <button
              onClick={this.addFile.bind(this)}
              className="px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors"
            >
              {i18next.t("general:Add")}
            </button>
            {this.state.selectedRowKeys.length > 0 && (
              <button
                onClick={() => {
                  if (window.confirm(`${i18next.t("general:Sure to delete")}: ${this.state.selectedRowKeys.length} ${i18next.t("general:items")} ?`)) {
                    this.performBulkDelete(this.state.selectedRows, this.state.selectedRowKeys);
                  }
                }}
                className="px-3 py-1 bg-red-600 text-white rounded text-xs font-medium hover:bg-red-700 transition-colors flex items-center gap-1"
              >
                <Trash2 className="w-3 h-3" />
                {i18next.t("general:Delete")} ({this.state.selectedRowKeys.length})
              </button>
            )}
          </div>
          <span className="text-xs text-zinc-500">
            {i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total)}
          </span>
        </div>

        {this.state.loading ? (
          <div className="flex justify-center py-12">
            <Loader2 className="w-8 h-8 text-zinc-500 animate-spin" />
          </div>
        ) : (
          <div className="overflow-x-auto border border-zinc-800 rounded-lg">
            <table className="w-full text-sm text-left">
              <thead className="bg-zinc-900/80 border-b border-zinc-800">
                <tr>
                  <th className="px-3 py-2">
                    <input
                      type="checkbox"
                      className="rounded bg-zinc-800 border-zinc-700"
                      checked={this.state.selectedRowKeys.length === files?.length && files?.length > 0}
                      onChange={(e) => {
                        if (e.target.checked) {
                          this.onSelectAll(true, files);
                        } else {
                          this.clearSelection();
                        }
                      }}
                    />
                  </th>
                  {filteredColumns.map(col => (
                    <th key={col.key} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">
                      {col.title}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/50">
                {files?.map((record, index) => (
                  <tr key={record.name} className="hover:bg-zinc-900/50 transition-colors">
                    <td className="px-3 py-2">
                      <input
                        type="checkbox"
                        className="rounded bg-zinc-800 border-zinc-700"
                        checked={this.state.selectedRowKeys.includes(this.getRowKey(record))}
                        onChange={(e) => {
                          const key = this.getRowKey(record);
                          if (e.target.checked) {
                            this.onSelectChange(
                              [...this.state.selectedRowKeys, key],
                              [...this.state.selectedRows, record]
                            );
                          } else {
                            this.onSelectChange(
                              this.state.selectedRowKeys.filter(k => k !== key),
                              this.state.selectedRows.filter(r => this.getRowKey(r) !== key)
                            );
                          }
                        }}
                      />
                    </td>
                    {filteredColumns.map(col => (
                      <td key={col.key} className="px-3 py-2 text-zinc-300 whitespace-nowrap">
                        {col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    );
  }

  fetch = (params = {}) => {
    this.setState({loading: true});
    const store = this.state.storeName;
    FileBackend.getFiles("admin", store)
      .then((res) => {
        this.setState({loading: false});
        if (res.status === "ok") {
          this.setState({
            data: res.data,
            pagination: {
              ...params.pagination,
              total: res.data?.length || 0,
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

export default FileListPage;
