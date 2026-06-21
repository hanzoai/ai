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

import {Input} from "antd";
import {ChevronDown, ChevronUp, Plus, Trash2} from "lucide-react";
import i18next from "i18next";
import React from "react";
import * as Setting from "../Setting";

class ExampleQuestionTable extends React.Component {
  constructor(props) {
    super(props);
  }

  updateTable(table) {
    this.props.onUpdateTable(table);
  }

  updateField(table, index, key, value) {
    table[index][key] = value;
    this.updateTable(table);
  }

  addRow(table) {
    const row = {
      title: "Example Question",
      text: "What can you help me with?",
      image: "",
    };
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

  render() {
    if (!this.props.table) {
      this.props.onUpdateTable([]);
    }

    const columns = [
      {
        title: i18next.t("general:Title"),
        dataIndex: "title",
        key: "title",
        width: "30%",
        render: (text, record, index) => (
          <Input value={text} onChange={e => this.updateField(this.props.table, index, "title", e.target.value)} />
        ),
      },
      {
        title: i18next.t("general:Text"),
        dataIndex: "text",
        key: "text",
        width: "30%",
        render: (text, record, index) => (
          <Input value={text} onChange={e => this.updateField(this.props.table, index, "text", e.target.value)} />
        ),
      },
      {
        title: i18next.t("general:Icon"),
        dataIndex: "image",
        key: "image",
        width: "30%",
        render: (text, record, index) => (
          <Input
            value={text}
            onChange={e => this.updateField(this.props.table, index, "image", e.target.value)}
            placeholder={i18next.t("store:Icon URL (optional)")}
          />
        ),
      },
      {
        title: i18next.t("general:Action"),
        key: "action",
        width: "100px",
        render: (text, record, index) => {
          return (
            <div className="flex items-center gap-1">
              <button
                className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50"
                disabled={index === 0}
                title={i18next.t("general:Up")}
                onClick={() => this.upRow(this.props.table, index)}
              >
                <ChevronUp className="w-3.5 h-3.5" />
              </button>
              <button
                className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50"
                disabled={index === this.props.table.length - 1}
                title={i18next.t("general:Down")}
                onClick={() => this.downRow(this.props.table, index)}
              >
                <ChevronDown className="w-3.5 h-3.5" />
              </button>
              <button
                className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"
                title={i18next.t("general:Delete")}
                onClick={() => this.deleteRow(this.props.table, index)}
              >
                <Trash2 className="w-3.5 h-3.5" />
              </button>
            </div>
          );
        },
      },
    ];

    return (
      <div style={{marginTop: "20px"}}>
        <div className="flex items-center gap-3 mb-2">
          <span className="text-sm font-medium text-zinc-300">{i18next.t("store:Example questions")}</span>
          <button
            className="inline-flex items-center gap-1 px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200"
            onClick={() => this.addRow(this.props.table)}
          >
            <Plus className="w-3.5 h-3.5" />
            {i18next.t("general:Add")}
          </button>
        </div>
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(this.props.table || []).map((record, index) => <tr key={record.index || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
      </div>
    );
  }
}

export default ExampleQuestionTable;
