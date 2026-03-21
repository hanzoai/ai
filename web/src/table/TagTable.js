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
import i18next from "i18next";
import * as MessageBackend from "../backend/MessageBackend";

class TagTable extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      selectedRowKeys: [],
    };
  }

  updateTable(table) {
    this.props.onUpdateTable(table);
  }

  parseField(key, value) {
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

  updateVideoField(key, value) {
    this.props.onUpdateVideoField(key, value);
  }

  getQuestion(task, example) {
    return `${task.scale.replace("{example}", example).replace("{labels}", task.labels.map(label => `"${label}"`).join(", "))}`;
  }

  trimAnswer(s) {
    let res = Setting.trim(s, "[");
    res = Setting.trim(res, "]");
    res = Setting.trim(res, "\"");
    return res;
  }

  getAnswer(task, text, rowIndex, columnIndex) {
    const provider = task.provider;
    const question = this.getQuestion(task, text);
    const framework = task.name;
    const video = this.props.video.name;
    MessageBackend.getAnswer(provider, question, framework, video)
      .then((res) => {
        if (res.status === "ok") {
          this.updateField(this.props.table, rowIndex, `tag${columnIndex + 1}`, this.trimAnswer(res.data));
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  runTask(columnIndex) {
    const task = this.props.tasks[columnIndex];
    this.props.table.forEach((row, rowIndex) => {
      if (!this.state.selectedRowKeys.includes(`${rowIndex}`)) {
        return;
      }

      this.getAnswer(task, row.text, rowIndex, columnIndex);
    });
  }

  clearTask(columnIndex) {
    this.props.table.forEach((row, rowIndex) => {
      if (!this.state.selectedRowKeys.includes(`${rowIndex}`)) {
        return;
      }

      this.updateField(this.props.table, rowIndex, `tag${columnIndex + 1}`, "");
    });
  }

  renderTableHeader(columnIndex) {
    const taskFieldName = `task${columnIndex + 1}`;
    return (
      <React.Fragment>
        <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.props.video[taskFieldName]}> {this.updateVideoField(taskFieldName, value);})}
          options={this.props.tasks.map((task) => Setting.getOption(task.displayName, task.name))
          } />
        &nbsp;&nbsp;
        
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={this.props.video[taskFieldName] === "" || this.state.selectedRowKeys.length === 0}>} size="small" onClick={() => this.runTask(columnIndex)} />
        
        &nbsp;&nbsp;
        
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small" onClick={() => {
            this.clearTask(columnIndex);
            this.updateVideoField(taskFieldName, "");
          }} />
        
      </React.Fragment>
    );
  }

  renderTable(table) {
    const columns = [
      {
        title: i18next.t("general:No."),
        dataIndex: "no",
        key: "no",
        width: "50px",
        render: (text, record, index) => {
          return (
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{width: "50px"}> {
              this.props.player.seek(record.startTime);
              this.props.screen.clear();
              this.props.videoObj.clearMaps();
            }} >
              {index + 1}
            </button>
          );
        },
      },
      {
        title: i18next.t("video:Time"),
        dataIndex: "startTime",
        key: "startTime",
        width: "100px",
        render: (text, record, index) => {
          return Setting.getTimeFromSeconds(text);
        },
      },
      {
        title: i18next.t("video:Role"),
        dataIndex: "speaker",
        key: "speaker",
        width: "50px",
        render: (text, record, index) => {
          return Setting.getSpeakerTag(text);
        },
      },
      {
        title: i18next.t("general:Text"),
        dataIndex: "text",
        key: "text",
        // width: "200px",
      },
      {
        title: this.renderTableHeader(0),
        dataIndex: "tag1",
        key: "tag1",
        width: "250px",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "tag1", e.target.value);
            }} />
          );
        },
      },
      {
        title: this.renderTableHeader(1),
        dataIndex: "tag2",
        key: "tag2",
        width: "250px",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "tag2", e.target.value);
            }} />
          );
        },
      },
      {
        title: this.renderTableHeader(2),
        dataIndex: "tag3",
        key: "tag3",
        width: "250px",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "tag3", e.target.value);
            }} />
          );
        },
      },
    ];

    const rowSelection = {
      onChange: (newSelectedRowKeys) => {
        this.setState({
          selectedRowKeys: newSelectedRowKeys,
        });
      },
      selections: [
        Table.SELECTION_ALL,
        Table.SELECTION_INVERT,
        Table.SELECTION_NONE,
      ],
    };

    return (
      <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(table || []).map((record, index) => <tr key={typeof {"id"} === "function" ? ({"id"})(record) : record[{"id"}] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
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

export default TagTable;
