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
import * as Setting from "../Setting";
import i18next from "i18next";
import * as PermissionUtil from "../PermissionUtil";
import * as TreeFileBackend from "../backend/TreeFileBackend";

class FileTable extends React.Component {
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
    const row = {no: table.length, name: `New Factor - ${table.length}`, data: []};
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

  deleteFile(file, isLeaf) {
    const storeId = `${this.props.store.owner}/${this.props.store.name}`;
    TreeFileBackend.deleteFile(storeId, file.key, isLeaf)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data === true) {
            Setting.showMessage("success", i18next.t("general:Successfully deleted"));
            this.props.onRefresh();
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${error}`);
      });
  }

  renderTable(table) {
    const columns = [
      {
        title: i18next.t("store:File name"),
        dataIndex: "title",
        key: "title",
        // width: '200px',
        sorter: (a, b) => a.title.localeCompare(b.title),
        render: (text, record, index) => {
          return text;
        },
      },
      {
        title: i18next.t("general:Category"),
        dataIndex: "isLeaf",
        key: "isLeaf",
        width: "110px",
        sorter: (a, b) => a.isLeaf - b.isLeaf,
        filters: Setting.getDistinctArray(table.map(record => Setting.getFileCategory(record)))
          .map(fileType => {
            return {text: fileType, value: fileType};
          }),
        onFilter: (value, record) => Setting.getFileCategory(record) === value,
        render: (text, record, index) => {
          return Setting.getFileCategory(record);
        },
      },
      {
        title: i18next.t("store:File type"),
        dataIndex: "fileType",
        key: "fileType",
        width: "140px",
        sorter: (a, b) => Setting.getExtFromPath(a.title).localeCompare(Setting.getExtFromPath(b.title)),
        filters: Setting.getDistinctArray(table.map(record => Setting.getExtFromFile(record)))
          .filter(fileType => fileType !== "")
          .map(fileType => {
            return {text: fileType, value: fileType};
          }),
        onFilter: (value, record) => Setting.getExtFromFile(record) === value,
        render: (text, record, index) => {
          return Setting.getExtFromFile(record);
        },
      },
      {
        title: i18next.t("store:File size"),
        dataIndex: "size",
        key: "size",
        width: "120px",
        sorter: (a, b) => a.size - b.size,
        render: (text, record, index) => {
          if (!record.isLeaf) {
            return null;
          }

          return Setting.getFriendlyFileSize(text);
        },
      },
      {
        title: i18next.t("general:Created time"),
        dataIndex: "createdTime",
        key: "createdTime",
        width: "160px",
        sorter: (a, b) => a.createdTime.localeCompare(b.createdTime),
        render: (text, record, index) => {
          return Setting.getFormattedDate(text);
        },
      },
      {
        title: i18next.t("store:Collected time"),
        dataIndex: "collectedTime",
        key: "collectedTime",
        width: "160px",
        sorter: (a, b) => a.collectedTime.localeCompare(b.collectedTime),
        render: (text, record, index) => {
          const collectedTime = Setting.getCollectedTime(record.title);
          return Setting.getFormattedDate(collectedTime);
        },
      },
      {
        title: i18next.t("store:Subject"),
        dataIndex: "subject",
        key: "subject",
        width: "90px",
        sorter: (a, b) => a.subject.localeCompare(b.subject),
        render: (text, record, index) => {
          return Setting.getSubject(record.title);
        },
      },
      // {
      //   title: i18next.t("provider:Path"),
      //   dataIndex: 'key',
      //   key: 'key',
      //   width: '100px',
      //   sorter: (a, b) => a.key.localeCompare(b.key),
      // },
    ];

    return (
      <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(table || []).map((record, index) => <tr key={typeof "title" === "function" ? ("title")(record) : record["title"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
    );
  }

  render() {
    return (
      <div>
        {
          this.renderTable(this.props.file.children)
        }
      </div>
    );
  }
}

export default FileTable;
