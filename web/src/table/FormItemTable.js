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

class FormItemTable extends React.Component {
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

  addRow(table) {
    const row = {name: `column${table.length}`, label: `Column ${table.length}`, type: "Text", visible: true, width: "100"};
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

  defaultTable() {
    let rows = this.getItems();
    if (!Array.isArray(rows)) {
      rows = [rows];
    }
    this.updateTable(rows);
  }

  getItems() {
    const formType = this.props.formType;
    return Setting.getFormTypeItems(formType);
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
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "name", e.target.value);
            }} />
          );
        },
      },
      {
        title: i18next.t("general:Label"),
        dataIndex: "label",
        key: "label",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "label", e.target.value);
            }} />
          );
        },
      },
      {
        title: i18next.t("general:Type"),
        dataIndex: "type",
        key: "type",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "type", e.target.value);
            }} />
          );
        },
      },
      {
        title: i18next.t("form:Width"),
        dataIndex: "width",
        key: "width",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "width", e.target.value);
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
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={index === 0} style={{marginRight: "5px"}>} size="small" onClick={() => this.upRow(table, index)} />
              
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={index === table.length - 1} style={{marginRight: "5px"}>} size="small" onClick={() => this.downRow(table, index)} />
              
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small" onClick={() => this.deleteRow(table, index)} />
              
            </div>
          );
        },
      },
    ];

    return (
      <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(table || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
    );
  }

  renderListPageTable(table) {
    const columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "200px",
        render: (text, record, index) => {
          const items = this.getItems();
          const options = Setting.getDeduplicatedArray(items, table, "name").map(item => ({label: i18next.t(item.label), value: item.name}));
          const selectedLabel = items.find(item => item.name === text)?.label || text;
          return (
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={i18next.t(selectedLabel)}> {
                this.updateField(table, index, "name", value);
              }}
              optionLabelProp="label"
            />
          );
        },
      },
      {
        title: i18next.t("general:Label"),
        dataIndex: "label",
        key: "label",
        width: "200px",
        render: (text, record, index) => {
          const items = this.getItems();
          const selectedItem = items.find(item => item.name === text);
          const currentLabel = selectedItem?.label || text;
          return (
            <Input
              value={i18next.t(currentLabel)}
              onChange={e => {
                const newLabel = e.target.value;
                this.updateField(this.props.table, index, "label", newLabel);
                if (selectedItem) {
                  selectedItem.label = newLabel;
                }
              }}
            />
          );
        },
      },
      {
        title: i18next.t("general:Visible"),
        dataIndex: "visible",
        key: "visible",
        width: "200px",
        render: (text, record, index) => {
          return (
            <span className="px-2 py-0.5 rounded text-xs " + (text ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{text ? "ON" : "OFF"}</span>
          );
        },
      },
      {
        title: i18next.t("form:Width"),
        dataIndex: "width",
        key: "width",
        render: (text, record, index) => {
          return (
            <Input value={text} onChange={e => {
              this.updateField(table, index, "width", e.target.value);
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
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={index === 0} style={{marginRight: "5px"}>}
                  size="small" onClick={() => this.upRow(table, index)} />
              
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700 disabled:opacity-50" disabled={index === table.length - 1} style={{marginRight: "5px"}>} size="small" onClick={() => this.downRow(table, index)} />
              
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small" onClick={() => this.deleteRow(table, index)} />
              
            </div>
          );
        },
      },
    ];

    return (
      <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(table || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
    );
  }

  render() {
    return (
      <div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {
              this.props.category === "List Page" ? this.renderListPageTable(this.props.table) : this.renderTable(this.props.table)
            }
          </div>
        </div>
      </div>
    );
  }
}

export default FormItemTable;
