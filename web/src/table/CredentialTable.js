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
import copy from "copy-to-clipboard";
import * as Setting from "../Setting";


class CredentialsTable extends React.Component {
  copyToClipboard = (text) => {
    copy(text);
    Setting.showMessage("success", i18next.t("general:Successfully copied"));
  };

  getColumns = () => {
    return [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "200px",
        sorter: (a, b) => a.name.localeCompare(b.name),
        render: (text) => <span className="text-zinc-300 text-sm">{text}</span>,
      },
      {
        title: i18next.t("general:Data"),
        dataIndex: "value",
        key: "value",
        width: "300px",
        render: (text) => (
          <span className="text-zinc-300 text-sm">{text}</span>
        ),
      },
      {
        title: i18next.t("general:Action"),
        key: "action",
        width: "80px",
        render: (text, record) => (
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">}
            size="small"
            disabled={!record.value}
            onClick={() => this.copyToClipboard(record.value)}
          >
            {i18next.t("general:Copy")}
          </button>
        ),
      },
    ];
  };

  render() {
    const {credentials} = this.props;

    if (!credentials || credentials.length === 0) {
      return null;
    }

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{this.getColumns().map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(credentials || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{this.getColumns().map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
      </div>
    );
  }
}

export default CredentialsTable;
