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
import moment from "moment";
import BaseListPage from "./BaseListPage";
import * as Setting from "./Setting";
import * as ProviderBackend from "./backend/ProviderBackend";
import i18next from "i18next";
import * as Provider from "./Provider";
import {Trash2, Loader2} from "lucide-react";

class ProviderListPage extends BaseListPage {
  constructor(props) {
    super(props);
  }

  newProvider() {
    const randomName = Setting.getRandomName();
    return {
      owner: "admin",
      name: `provider_${randomName}`,
      createdTime: moment().format(),
      displayName: `New Provider - ${randomName}`,
      category: "Model",
      type: "OpenAI",
      subType: "text-davinci-003",
      clientId: "",
      clientSecret: "",
      mcpTools: [],
      enableThinking: false,
      temperature: 1,
      topP: 1,
      topK: 4,
      frequencyPenalty: 0,
      presencePenalty: 0,
      inputPricePerThousandTokens: 0.0,
      outputPricePerThousandTokens: 0.0,
      currency: "USD",
      providerUrl: "https://platform.openai.com/account/api-keys",
      apiVersion: "",
      apiKey: "",
      network: "",
      userKey: "",
      userCert: "",
      signKey: "",
      signCert: "",
      compatibleProvider: "",
      contractName: "",
      contractMethod: "",
      state: "Active",
      isRemote: false,
    };
  }

  newStorageProvider() {
    const randomName = Setting.getRandomName();
    return {
      owner: "admin",
      name: `provider_${randomName}`,
      createdTime: moment().format(),
      displayName: `New Provider - ${randomName}`,
      category: "Storage",
      type: "Local File System",
      subType: "",
      clientId: "C:/storage_cloud",
      providerUrl: "",
      state: "Active",
      isRemote: false,
    };
  }

  addProvider() {
    const newProvider = this.newProvider();
    ProviderBackend.addProvider(newProvider)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({pathname: `/providers/${newProvider.name}`, state: {isNewProvider: true}});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  deleteItem = async(i) => {
    return ProviderBackend.deleteProvider(this.state.data[i]);
  };

  deleteProvider(record) {
    ProviderBackend.deleteProvider(record)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deleted"));
          this.setState({
            data: this.state.data.filter((item) => item.name !== record.name),
            pagination: {...this.state.pagination, total: this.state.pagination.total - 1},
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${error}`);
      });
  }

  renderTable(providers) {
    const columns = [
      {title: i18next.t("general:Name"), dataIndex: "name", key: "name", render: (text) => <Link to={`/providers/${text}`} className="text-blue-400 hover:text-blue-300">{text}</Link>},
      {title: i18next.t("general:Display name"), dataIndex: "displayName", key: "displayName"},
      {title: i18next.t("general:Category"), dataIndex: "category", key: "category"},
      {title: i18next.t("general:Type"), dataIndex: "type", key: "type", render: (text, record) => Provider.getProviderLogoWidget(record)},
      {title: i18next.t("provider:Sub type"), dataIndex: "subType", key: "subType"},
      {title: i18next.t("provider:Client ID"), dataIndex: "clientId", key: "clientId"},
      {title: i18next.t("general:Secret key"), dataIndex: "clientSecret", key: "clientSecret"},
      {title: i18next.t("general:Region"), dataIndex: "region", key: "region"},
      {title: i18next.t("provider:API key"), dataIndex: "apiKey", key: "apiKey"},
      {title: i18next.t("general:Provider URL"), dataIndex: "providerUrl", key: "providerUrl", render: (text) => <a target="_blank" rel="noreferrer" href={text} className="text-blue-400 hover:text-blue-300">{Setting.getShortText(text, 80)}</a>},
      {title: i18next.t("store:Is default"), dataIndex: "isDefault", key: "isDefault", render: (text) => (
        <span className={`px-2 py-0.5 rounded text-xs ${text ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500"}`}>{text ? i18next.t("general:ON") : i18next.t("general:OFF")}</span>
      )},
      {title: i18next.t("provider:Is remote"), dataIndex: "isRemote", key: "isRemote", render: (text) => (
        <span className={`px-2 py-0.5 rounded text-xs ${text ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500"}`}>{text ? i18next.t("general:ON") : i18next.t("general:OFF")}</span>
      )},
      {title: i18next.t("general:State"), dataIndex: "state", key: "state"},
      {title: i18next.t("general:Action"), dataIndex: "action", key: "action", render: (text, record, index) => (
        <div className="flex flex-wrap gap-2">
          <button onClick={() => this.props.history.push(`/providers/${record.name}`)} className={`px-3 py-1.5 rounded text-xs font-medium transition-colors ${record.isRemote ? "bg-zinc-800 text-zinc-300 hover:bg-zinc-700" : "bg-white text-black hover:bg-zinc-200"}`}>
            {record.isRemote ? i18next.t("general:View") : i18next.t("general:Edit")}
          </button>
          {!record.isRemote && (
            <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${record.name} ?`)) {this.deleteProvider(record);} }} className="px-3 py-1.5 bg-red-600 text-white rounded text-xs font-medium hover:bg-red-700 transition-colors">
              {i18next.t("general:Delete")}
            </button>
          )}
        </div>
      )},
    ];

    return (
      <div>
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-3">
            <h2 className="text-lg font-semibold text-white">{i18next.t("general:Providers")}</h2>
            <button onClick={() => this.addProvider()} className="px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Add")}</button>
            {this.state.selectedRowKeys.length > 0 && (
              <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${this.state.selectedRowKeys.length} ${i18next.t("general:items")} ?`)) {this.performBulkDelete(this.state.selectedRows, this.state.selectedRowKeys);} }} className="px-3 py-1 bg-red-600 text-white rounded text-xs font-medium hover:bg-red-700 transition-colors flex items-center gap-1">
                <Trash2 className="w-3 h-3" />{i18next.t("general:Delete")} ({this.state.selectedRowKeys.length})
              </button>
            )}
          </div>
          <span className="text-xs text-zinc-500">{i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total)}</span>
        </div>
        {this.state.loading ? (
          <div className="flex justify-center py-12"><Loader2 className="w-8 h-8 text-zinc-500 animate-spin" /></div>
        ) : (
          <div className="overflow-x-auto border border-zinc-800 rounded-lg">
            <table className="w-full text-sm text-left">
              <thead className="bg-zinc-900/80 border-b border-zinc-800">
                <tr>
                  <th className="px-3 py-2"><input type="checkbox" className="rounded bg-zinc-800 border-zinc-700" checked={this.state.selectedRowKeys.length === providers?.length && providers?.length > 0} onChange={(e) => { if (e.target.checked) {this.onSelectAll(true, providers);} else {this.clearSelection();} }} /></th>
                  {columns.map(col => <th key={col.key} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/50">
                {providers?.map((record, index) => (
                  <tr key={record.name} className="hover:bg-zinc-900/50 transition-colors">
                    <td className="px-3 py-2">
                      <input type="checkbox" className="rounded bg-zinc-800 border-zinc-700" checked={this.state.selectedRowKeys.includes(this.getRowKey(record))} onChange={(e) => {
                        const key = this.getRowKey(record);
                        if (e.target.checked) {this.onSelectChange([...this.state.selectedRowKeys, key], [...this.state.selectedRows, record]);} else {this.onSelectChange(this.state.selectedRowKeys.filter(k => k !== key), this.state.selectedRows.filter(r => this.getRowKey(r) !== key));}
                      }} />
                    </td>
                    {columns.map(col => <td key={col.key} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}
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
    let field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    if (params.category !== undefined && params.category !== null) {
      field = "category";
      value = params.category;
    } else if (params.type !== undefined && params.type !== null) {
      field = "type";
      value = params.type;
    }
    this.setState({loading: true});
    ProviderBackend.getProviders(this.props.account.name, Setting.getRequestStore(this.props.account), params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
      .then((res) => {
        this.setState({loading: false});
        if (res.status === "ok") {
          this.setState({
            data: res.data,
            pagination: {...params.pagination, total: res.data2},
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

export default ProviderListPage;
