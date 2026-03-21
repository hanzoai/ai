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
import * as Setting from "../Setting";
import i18next from "i18next";
import moment from "moment/moment";
import * as Conf from "../Conf";


class RemarkTable extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
    };
  }

  updateTable(table) {
    this.props.onUpdateTable(table);
  }

  parseField(key, value) {
    if (key === "data") {
      value = Setting.trim(value, ",");
      return value.split(",").map(i => Setting.myParseInt(i));
    }

    if ([].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateField(table, index, key, value) {
    value = this.parseField(key, value);

    table[index][key] = value;
    this.updateTable(table);
  }

  addRow(table) {
    const row = {no: table.length, timestamp: moment().format(), user: this.props.account.name};
    if (table === undefined) {
      table = [];
    }
    table = Setting.addRow(table, row);
    this.updateTable(table);
  }

  deleteRow(table, i) {
    table = Setting.deleteRow(table, i);
    this.updateTable(table);
  }

  upRow(table, i) {
    table = Setting.swapRow(table, i - 1, i);
    this.updateTable(table);
  }

  downRow(table, i) {
    table = Setting.swapRow(table, i, i + 1);
    this.updateTable(table);
  }

  requireSelfOrAdmin(row) {
    if (!this.requireAdmin()) {
      return false;
    }

    return !(row.user === this.props.account.name);
  }

  requireSelfOrAdminForView(row) {
    if (this.props.title !== i18next.t("video:Remarks1") || this.props.account.type !== "video-reviewer1-user") {
      return false;
    }

    return !(row.user === this.props.account.name);
  }

  filterRemark1Value(row, value) {
    if (this.requireSelfOrAdminForView(row)) {
      if (typeof value === "boolean") {
        return false;
      } else {
        return "***";
      }
    } else {
      return value;
    }
  }

  requireReviewer() {
    return !(this.props.account.type === "video-reviewer1-user" || this.props.account.type === "video-reviewer2-user");
  }

  requireAdmin() {
    if (Setting.isAdminUser(this.props.account)) {
      return false;
    }

    return !(this.props.account.type === "video-admin-user");
  }

  renderTable(table) {
    const columns = [
      {
        title: i18next.t("general:No."),
        dataIndex: "no",
        key: "no",
        width: "60px",
        render: (text, record, index) => {
          return (index + 1);
        },
      },
      {
        title: i18next.t("video:Time"),
        dataIndex: "timestamp",
        key: "timestamp",
        width: "160px",
        render: (text, record, index) => {
          return Setting.getFormattedDate(text);
        },
      },
      {
        title: i18next.t("general:User"),
        dataIndex: "user",
        key: "user",
        width: "110px",
        render: (text, record, index) => {
          if (this.requireSelfOrAdminForView(record)) {
            return "***";
          }

          return (
            <a target="_blank" rel="noreferrer" href={Setting.getMyProfileUrl(this.props.account).replace("/account", `/users/${Conf.AuthConfig.organizationName}/${text}`)}>
              {text}
            </a>
          );
        },
      },
      {
        title: i18next.t("video:Score"),
        dataIndex: "score",
        key: "score",
        width: "130px",
        render: (text, record, index) => {
          return (
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.filterRemark1Value(record, text)} disabled> {
              this.updateField(table, index, "score", value);
            })}>
              {
                [
                  {id: "Excellent"},
                  {id: "Good"},
                  {id: "Pass"},
                  {id: "Fail"},
                ].map((item, index) => <option key={index} value={item.id}>{Setting.getRemarkTag(item.id)}</option>)
              }
            </select>
          );
        },
      },
      {
        title: i18next.t("video:Is recommended"),
        dataIndex: "isPublic",
        key: "isPublic",
        width: "110px",
        render: (text, record, index) => {
          return (
            <span className="px-2 py-0.5 rounded text-xs " + (this.filterRemark1Value(record, text) ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{this.filterRemark1Value(record, text) ? "ON" : "OFF"}</span>
          );
        },
      },
      {
        title: i18next.t("message:Comment"),
        dataIndex: "text",
        key: "text",
        // width: '300px',
        render: (text, record, index) => {
          return (
            <span className="text-zinc-300 text-sm"> {
              this.updateField(table, index, "text", e.target.value);
            }} />
          );
        },
      },
      {
        title: i18next.t("general:Action"),
        key: "action",
        width: "100px",
        render: (text, record, index) => {
          return (
            <div>
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={index === 0 || this.props.disabled || this.requireAdmin()} style={{marginRight: "5px"}>} size="small" onClick={() => this.upRow(table, index)} />
              
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={index === table.length - 1 || this.props.disabled || this.requireAdmin()} style={{marginRight: "5px"}>} size="small" onClick={() => this.downRow(table, index)} />
              
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small" disabled={this.props.disabled || this.requireSelfOrAdmin(record)} onClick={() => this.deleteRow(table, index)} />
              
            </div>
          );
        },
      },
    ];

    const myRowCount = table.filter((row) => row.user === this.props.account.name).length;

    return (
      <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(table || []).map((record, index) => <tr key={typeof "index" === "function" ? ("index")(record) : record["index"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
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

export default RemarkTable;
