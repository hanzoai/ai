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
import {Link} from "react-router-dom";
import BaseListPage from "./BaseListPage";
import moment from "moment";
import * as Setting from "./Setting";
import * as ImageBackend from "./backend/ImageBackend";
import * as MachineBackend from "./backend/MachineBackend";
import i18next from "i18next";
import {Trash2, Loader2} from "lucide-react";

class ImageListPage extends BaseListPage {
  constructor(props) {
    super(props);
  }

  newImage() {
    return {
      owner: this.props.account.owner,
      name: `image_${Setting.getRandomName()}`,
      category: "Private Image",
      imageId: "",
      state: "Available",
      tag: "",
      description: "",
      os: "Ubuntu_64",
      platform: "Ubuntu",
      systemArchitecture: "64bits",
      size: "5 GiB",
      bootMode: "BIOS",
      progress: "100%",
      createdTime: moment().format(),
      updatedTime: moment().format(),
    };
  }

  addImage() {
    const newImage = this.newImage();
    ImageBackend.addImage(newImage)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push({pathname: `/images/${newImage.owner}/${newImage.name}`, mode: "add"});
          Setting.showMessage("success", i18next.t("general:Successfully added"));
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  deleteItem = async(index) => {
    return ImageBackend.deleteImage(this.state.data[index]);
  };

  deleteImage(i) {
    ImageBackend.deleteImage(this.state.data[i])
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deleted"));
          this.setState({
            data: Setting.deleteRow(this.state.data, i),
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

  createMachine(image) {
    const newMachine = {
      owner: image.owner,
      name: image.name,
      provider: "",
      createdTime: image.createdTime,
      updatedTime: image.updatedTime,
      expireTime: "",
      displayName: image.imageId,
      region: "West US 2",
      zone: "Zone 1",
      category: "Standard",
      type: "Pay As You Go",
      size: image.size,
      tag: image.tag,
      state: "Inactive",
      image: image.os,
      publicIp: "",
      privateIp: "",
    };
    MachineBackend.addMachine(newMachine)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push({pathname: `/machines/${newMachine.owner}/${newMachine.name}`, mode: "add"});
          Setting.showMessage("success", i18next.t("general:Successfully added"));
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  renderTable(images) {
    const columns = [
      {title: i18next.t("general:Organization"), dataIndex: "owner", key: "owner", render: (text) => (
        <a target="_blank" rel="noreferrer" href={Setting.getMyProfileUrl(this.props.account).replace("/account", `/organizations/${text}`)} className="text-blue-400 hover:text-blue-300">{text}</a>
      )},
      {title: i18next.t("general:Name"), dataIndex: "name", key: "name", render: (text, record) => (
        <Link to={`/images/${record.owner}/${record.name}`} className="text-blue-400 hover:text-blue-300">{text}</Link>
      )},
      {title: i18next.t("general:Category"), dataIndex: "category", key: "category"},
      {title: i18next.t("general:ID"), dataIndex: "imageId", key: "imageId"},
      {title: i18next.t("general:State"), dataIndex: "state", key: "state"},
      {title: i18next.t("general:Tag"), dataIndex: "tag", key: "tag"},
      {title: i18next.t("general:Description"), dataIndex: "description", key: "description"},
      {title: i18next.t("node:OS"), dataIndex: "os", key: "os"},
      {title: i18next.t("image:Platform"), dataIndex: "platform", key: "platform"},
      {title: i18next.t("image:Arch"), dataIndex: "systemArchitecture", key: "systemArchitecture"},
      {title: i18next.t("general:Size"), dataIndex: "size", key: "size"},
      {title: i18next.t("image:Boot mode"), dataIndex: "bootMode", key: "bootMode"},
      {title: i18next.t("general:Progress"), dataIndex: "progress", key: "progress"},
      {title: i18next.t("general:Created time"), dataIndex: "createdTime", key: "createdTime", render: (text) => Setting.getFormattedDate(text)},
      {title: i18next.t("general:Action"), dataIndex: "action", key: "action", render: (text, image, index) => (
        <div className="flex flex-wrap gap-2">
          <button onClick={() => this.props.history.push(`/images/${image.owner}/${image.name}`)} className="px-3 py-1.5 bg-zinc-800 text-zinc-300 rounded text-xs hover:bg-zinc-700 transition-colors">{i18next.t("general:Edit")}</button>
          <button onClick={() => this.createMachine(image)} className="px-3 py-1.5 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors">{i18next.t("image:Create machine")}</button>
          {image.owner === this.props.account.owner && (
            <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${image.name} ?`)) {this.deleteImage(index);} }} className="px-3 py-1.5 bg-red-600 text-white rounded text-xs font-medium hover:bg-red-700 transition-colors">{i18next.t("general:Delete")}</button>
          )}
        </div>
      )},
    ];

    return (
      <div>
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-3">
            <h2 className="text-lg font-semibold text-white">{i18next.t("general:Images")}</h2>
            <button onClick={this.addImage.bind(this)} className="px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Add")}</button>
            {this.state.selectedRowKeys.length > 0 && (
              <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${this.state.selectedRowKeys.length} ${i18next.t("general:items")} ?`)) {this.performBulkDelete(this.state.selectedRows, this.state.selectedRowKeys);} }} className="px-3 py-1 bg-red-600 text-white rounded text-xs font-medium hover:bg-red-700 transition-colors flex items-center gap-1">
                <Trash2 className="w-3 h-3" />
                {i18next.t("general:Delete")} ({this.state.selectedRowKeys.length})
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
                  <th className="px-3 py-2"><input type="checkbox" className="rounded bg-zinc-800 border-zinc-700" checked={this.state.selectedRowKeys.length === images?.length && images?.length > 0} onChange={(e) => { if (e.target.checked) {this.onSelectAll(true, images);} else {this.clearSelection();} }} /></th>
                  {columns.map(col => <th key={col.key} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/50">
                {images?.map((record, index) => (
                  <tr key={`${record.owner}/${record.name}`} className="hover:bg-zinc-900/50 transition-colors">
                    <td className="px-3 py-2">
                      <input type="checkbox" className="rounded bg-zinc-800 border-zinc-700" checked={this.state.selectedRowKeys.includes(this.getRowKey(record))} onChange={(e) => {
                        const key = this.getRowKey(record);
                        if (e.target.checked) {this.onSelectChange([...this.state.selectedRowKeys, key], [...this.state.selectedRows, record]);} else {this.onSelectChange(this.state.selectedRowKeys.filter(k => k !== key), this.state.selectedRows.filter(r => this.getRowKey(r) !== key));}
                      }} />
                    </td>
                    {columns.map(col => <td key={col.key} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}
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
    let field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    if (params.type !== undefined && params.type !== null) {
      field = "type";
      value = params.type;
    }
    this.setState({loading: true});
    ImageBackend.getImages(Setting.getRequestOrganization(this.props.account), params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
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

export default ImageListPage;
