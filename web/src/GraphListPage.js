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
import * as GraphBackend from "./backend/GraphBackend";
import i18next from "i18next";
import {Trash2, Loader2} from "lucide-react";
import Editor from "./common/Editor";
import GraphDataPage from "./GraphDataPage";
import GraphChatDataPage from "./GraphChatDataPage";

class GraphListPage extends BaseListPage {
  constructor(props) {
    super(props);
    this.state = {
      ...this.state,
      hoveredText: null,
    };
  }

  newGraph() {
    const randomName = Setting.getRandomName();
    return {
      owner: this.props.account.name,
      name: `graph_${randomName}`,
      createdTime: moment().format(),
      displayName: `New Graph - ${randomName}`,
      category: "Default",
      layout: "force",
      text: "",
    };
  }

  addGraph() {
    const newGraph = this.newGraph();
    GraphBackend.addGraph(newGraph)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({pathname: `/graphs/${newGraph.name}`, state: {isNewGraph: true}});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  deleteItem = async(i) => {
    return GraphBackend.deleteGraph(this.state.data[i]);
  };

  deleteGraph(record) {
    GraphBackend.deleteGraph(record)
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

  renderTable(graphs) {
    const columns = [
      {title: i18next.t("general:Name"), dataIndex: "name", key: "name", render: (text) => <Link to={`/graphs/${text}`} className="text-blue-400 hover:text-blue-300">{text}</Link>},
      {title: i18next.t("general:Display name"), dataIndex: "displayName", key: "displayName"},
      {title: i18next.t("general:Created time"), dataIndex: "createdTime", key: "createdTime", render: (text) => Setting.getFormattedDate(text)},
      {title: i18next.t("general:Category"), dataIndex: "category", key: "category"},
      {title: i18next.t("graph:Layout"), dataIndex: "layout", key: "layout"},
      {title: i18next.t("graph:Threshold"), dataIndex: "density", key: "density"},
      {title: i18next.t("general:Store"), dataIndex: "store", key: "store"},
      {title: i18next.t("video:Start time (s)"), dataIndex: "startTime", key: "startTime", render: (text) => Setting.getFormattedDate(text)},
      {title: i18next.t("video:End time (s)"), dataIndex: "endTime", key: "endTime", render: (text) => Setting.getFormattedDate(text)},
      {title: i18next.t("general:Text"), dataIndex: "text", key: "text", render: (text) => (
        <div className="relative group">
          <div className="max-w-[300px] truncate text-zinc-400 cursor-pointer">{Setting.getShortText(text, 200)}</div>
        </div>
      )},
      {title: i18next.t("general:Preview"), dataIndex: "text", key: "preview", render: (text, record) => (
        <div style={{height: "240px", width: "100%"}}>
          {record.category === "Chats" ? (
            <GraphChatDataPage graphText={text} showBorder={false} />
          ) : (
            <GraphDataPage account={this.props.account} owner={record.owner} graphName={record.name} graphText={text} category={record.category} layout={record.layout} showLegend={false} />
          )}
        </div>
      )},
      {title: i18next.t("general:Action"), dataIndex: "action", key: "action", render: (text, record) => (
        <div className="flex flex-wrap gap-2">
          <button onClick={() => this.props.history.push(`/graphs/${record.name}`)} className="px-3 py-1.5 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Edit")}</button>
          <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${record.name} ?`)) {this.deleteGraph(record);} }} className="px-3 py-1.5 bg-red-600 text-white rounded text-xs font-medium hover:bg-red-700 transition-colors">{i18next.t("general:Delete")}</button>
        </div>
      )},
    ];
    const filteredColumns = Setting.filterTableColumns(columns, this.props.formItems ?? this.state.formItems);

    return (
      <div>
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-3">
            <h2 className="text-lg font-semibold text-white">{i18next.t("general:Graphs")}</h2>
            <button onClick={this.addGraph.bind(this)} className="px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Add")}</button>
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
                  <th className="px-3 py-2"><input type="checkbox" className="rounded bg-zinc-800 border-zinc-700" checked={this.state.selectedRowKeys.length === graphs?.length && graphs?.length > 0} onChange={(e) => { if (e.target.checked) {this.onSelectAll(true, graphs);} else {this.clearSelection();} }} /></th>
                  {filteredColumns.map(col => <th key={col.key} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/50">
                {graphs?.map((record, index) => (
                  <tr key={record.name} className="hover:bg-zinc-900/50 transition-colors">
                    <td className="px-3 py-2">
                      <input type="checkbox" className="rounded bg-zinc-800 border-zinc-700" checked={this.state.selectedRowKeys.includes(this.getRowKey(record))} onChange={(e) => {
                        const key = this.getRowKey(record);
                        if (e.target.checked) {this.onSelectChange([...this.state.selectedRowKeys, key], [...this.state.selectedRows, record]);} else {this.onSelectChange(this.state.selectedRowKeys.filter(k => k !== key), this.state.selectedRows.filter(r => this.getRowKey(r) !== key));}
                      }} />
                    </td>
                    {filteredColumns.map(col => <td key={col.key} className="px-3 py-2 text-zinc-300">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}
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
    const field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    this.setState({loading: true});
    GraphBackend.getGraphs(this.props.account.name, params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
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

export default GraphListPage;
