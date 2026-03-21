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
import i18next from "i18next";
import Editor from "../common/Editor";

class McpToolsTable extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
    };
  }

  updateTable(table) {
    this.props.onUpdateTable(table);
  }

  updateField(table, index, key, value) {
    table[index][key] = value;
    this.updateTable(table);
  }

  renderTable(table) {
    const columns = [
      {
        title: i18next.t("general:Is enabled"),
        dataIndex: "isEnabled",
        key: "isEnabled",
        width: "120px",
        render: (text, record, index) => {
          return (
            <span className="px-2 py-0.5 rounded text-xs " + (text ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{text ? "ON" : "OFF"}</span>
          );
        },
      },
      {
        title: i18next.t("provider:Server name"),
        dataIndex: "serverName",
        key: "serverName",
        width: "200px",
        sorter: (a, b) => a.serverName.localeCompare(b.serverName),
        render: (text, record, index) => {
          return (
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={text} readOnly />
          );
        },
      },
      {
        title: i18next.t("provider:Tools"),
        dataIndex: "tools",
        key: "tools",
        width: "800px",
        render: (text, record, index) => {
          const formattedTools = JSON.stringify(JSON.parse(record.tools), null, 2);
          return (
            <div style={{height: "490px", overflow: "auto"}}>
              <Editor
                value={formattedTools}
                lang="json"
                dark
                readOnly
              />
            </div>
          );
        },
      },
    ];

    return (
      <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(table || []).map((record, index) => <tr key={typeof "serverName" === "function" ? ("serverName")(record) : record["serverName"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
    );
  }

  render() {
    return (
      <div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {
              this.renderTable(this.props.table)
            }
          </div>
        </div>
      </div>
    );
  }
}

export default McpToolsTable;
