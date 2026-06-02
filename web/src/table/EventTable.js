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


class EventTable extends React.Component {
  copyEventDetails = (event) => {
    const details = [
      `${i18next.t("general:Type")}: ${event.type}`,
      `${i18next.t("general:Status")}: ${event.reason}`,
      `${i18next.t("general:Object")}: ${event.involvedObject}`,
      `${i18next.t("general:Message")}: ${event.message}`,
      `${i18next.t("message:Author")}: ${event.source}`,
      `${i18next.t("general:Count")}: ${event.count}`,
      `${i18next.t("general:Created time")}: ${event.firstTime}`,
      `${i18next.t("general:Updated time")}: ${event.lastTime}`,
    ].join("\n");

    copy(details);
    Setting.showMessage("success", i18next.t("general:Successfully copied"));
  };

  // Get table column definitions
  getColumns = () => {
    return [
      {
        title: i18next.t("general:Type"),
        dataIndex: "type",
        key: "type",
        width: "80px",
        render: (text) => {
          const color = text === "Warning" ? "orange" : text === "Normal" ? "green" : "default";
          return <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">{text}</span>;
        },
      },
      {
        title: i18next.t("general:Status"),
        dataIndex: "reason",
        key: "reason",
        width: "120px",
        render: (text) => <span className="text-zinc-300 text-sm">{text}</span>,
      },
      {
        title: i18next.t("general:Object"),
        dataIndex: "involvedObject",
        key: "involvedObject",
        width: "150px",
        render: (text) => (
          <span className="text-zinc-300 text-sm">
            {text}
          </span>
        ),
      },
      {
        title: i18next.t("general:Message"),
        dataIndex: "message",
        key: "message",
        width: "300px",
        render: (text) => (
          <div>
            <span className="text-zinc-300 text-sm">
              {text.length > 80 ? (
                
                  {text.substring(0, 80)}...
                
              ) : (
                text
              )}
            </span>
          </div>
        ),
      },
      {
        title: i18next.t("general:Count"),
        dataIndex: "count",
        key: "count",
        width: "60px",
        render: (text) => (
          <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs"> 1 ? "red" : "default"}>
            {text}
          </span>
        ),
      },
      {
        title: i18next.t("general:Updated time"),
        dataIndex: "lastTime",
        key: "lastTime",
        width: "140px",
        render: (text) => (
          <span className="text-zinc-300 text-sm">
            {text}
          </span>
        ),
      },
      {
        title: i18next.t("general:Action"),
        key: "action",
        width: "80px",
        render: (text, record) => (
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">}
            size="small"
            onClick={() => this.copyEventDetails(record)}
            title={i18next.t("general:Copy")}
          />
        ),
      },
    ];
  };

  render() {
    const {events} = this.props;

    if (!events || events.length === 0) {
      return null;
    }

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
            {i18next.t("general:Records")}
            <span className="text-zinc-300 text-sm">
              ({events.length})
            </span>
          </span>
        }
        style={{marginBottom: 16}}
      >
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{this.getColumns().map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(events || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{this.getColumns().map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
      </div>
    );
  }
}

export default EventTable;
