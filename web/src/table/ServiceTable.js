// Copyright 2024 Hanzo AI Inc. All Rights Reserved.
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


class ServiceTable extends React.Component {
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
    if (["no", "port", "processId"].includes(key)) {
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
    const row = {no: table.length, name: `New Service - ${table.length}`, path: "C:/github_repos/cloud", port: 10000, processId: -1, expectedStatus: "Stopped", status: "", subStatus: "", message: ""};
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

  renderTable(table) {
    const columns = [
      {
        title: "No.",
        dataIndex: "no",
        key: "no",
        width: "60px",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "no", e.target.value);
            }} />
          );
        },
      },
      {
        title: "Name",
        dataIndex: "name",
        key: "name",
        width: "180px",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "name", e.target.value);
            }} />
          );
        },
      },
      {
        title: "Path",
        dataIndex: "path",
        key: "path",
        width: "300px",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "path", e.target.value);
            }} />
          );
        },
      },
      {
        title: "Port",
        dataIndex: "port",
        key: "port",
        width: "100px",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "port", e.target.value);
            }} />
          );
        },
      },
      {
        title: "Process ID",
        dataIndex: "processId",
        key: "processId",
        width: "100px",
        render: (text, record, index) => {
          if (text === -1) {
            return null;
          } else {
            return text;
          }
        },
      },
      {
        title: "Expected status",
        dataIndex: "expectedStatus",
        key: "expectedStatus",
        width: "150px",
        render: (text, record, index) => {
          return (
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={text}> {this.updateField(table, index, "expectedStatus", value);}}>
              {
                [
                  {id: "Running", name: "Running"},
                  {id: "Stopped", name: "Stopped"},
                ].map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
              }
            </select>
          );
        },
      },
      {
        title: "Status",
        dataIndex: "status",
        key: "status",
        width: "150px",
        render: (text, record, index) => {
          if (record.subStatus === "In Progress") {
            return (
              <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">} color="processing">{`${text} (${record.subStatus})`}</span>
            );
          } else if (record.status === "Running") {
            return (
              <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">} color="success">{text}</span>
            );
          } else if (record.status === "Stopped") {
            return (
              <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">} color="error">{text}</span>
            );
          } else {
            return `${text} (${record.subStatus})`;
          }
        },
      },
      // {
      //   title: 'Sub Status',
      //   dataIndex: 'subStatus',
      //   key: 'subStatus',
      //   render: (text, record, index) => {
      //     return (
      //       <Input value={text} onChange={e => {
      //         this.updateField(table, index, 'subStatus', e.target.value);
      //       }} />
      //     )
      //   }
      // },
      {
        title: "Message",
        dataIndex: "message",
        key: "message",
      },
      {
        title: "Action",
        key: "action",
        width: "100px",
        render: (text, record, index) => {
          return (
            <div>
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={index === 0} style={{marginRight: "5px"}>} size="small" onClick={() => this.upRow(table, index)} />
              
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={index === table.length - 1} style={{marginRight: "5px"}>} size="small" onClick={() => this.downRow(table, index)} />
              
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small" onClick={() => this.deleteRow(table, index)} />
              
            </div>
          );
        },
      },
    ];

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

export default ServiceTable;
