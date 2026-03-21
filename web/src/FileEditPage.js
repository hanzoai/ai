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
import * as Setting from "./Setting";
import * as FileBackend from "./backend/FileBackend";

class FileEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      fileName: decodeURIComponent(props.match.params.fileName),
      isNewFile: props.location?.state?.isNewFile || false,
      file: null,
    };
  }

  UNSAFE_componentWillMount() {
    this.getFile();
  }

  getFile() {
    const fileName = this.state.fileName;
    FileBackend.getFile("admin", fileName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({file: res.data, fileName: fileName});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseFileField(key, value) {
    if (["size", "tokenCount"].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateFileField(key, value) {
    value = this.parseFileField(key, value);
    const file = this.state.file;
    file[key] = value;
    this.setState({file: file});
  }

  renderFile() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg">
        <div className="flex items-center justify-between px-6 py-4 border-b border-zinc-800">
          <h3 className="text-base font-medium text-white">{i18next.t("file:Edit File")}</h3>
          <div className="flex gap-2">
            <button onClick={() => this.submitFileEdit(false)} className="px-4 py-1.5 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Save")}</button>
            <button onClick={() => this.submitFileEdit(true)} className="px-4 py-1.5 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Save & Exit")}</button>
            {this.state.isNewFile && <button onClick={() => this.cancelFileEdit()} className="px-4 py-1.5 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Cancel")}</button>}
          </div>
        </div>
        <div className="p-6 space-y-5">
          <div className="flex flex-col sm:flex-row gap-2">
            <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))}</label>
            <input className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500" value={this.state.file.name} onChange={e => this.updateFileField("name", e.target.value)} />
          </div>
          <div className="flex flex-col sm:flex-row gap-2">
            <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(i18next.t("file:Filename"), i18next.t("file:Filename - Tooltip"))}</label>
            <input className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500" value={this.state.file.filename} onChange={e => this.updateFileField("filename", e.target.value)} />
          </div>
          <div className="flex flex-col sm:flex-row gap-2">
            <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(i18next.t("general:Size"), i18next.t("general:Size - Tooltip"))}</label>
            <input type="number" className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500" value={this.state.file.size} onChange={e => this.updateFileField("size", e.target.value)} />
          </div>
          <div className="flex flex-col sm:flex-row gap-2">
            <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(i18next.t("general:Store"), i18next.t("general:Store - Tooltip"))}</label>
            <input className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500" value={this.state.file.store} onChange={e => this.updateFileField("store", e.target.value)} />
          </div>
          <div className="flex flex-col sm:flex-row gap-2">
            <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(i18next.t("store:Storage provider"), i18next.t("store:Storage provider - Tooltip"))}</label>
            <input className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500" value={this.state.file.storageProvider} onChange={e => this.updateFileField("storageProvider", e.target.value)} />
          </div>
          <div className="flex flex-col sm:flex-row gap-2">
            <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(i18next.t("chat:Token count"), i18next.t("chat:Token count - Tooltip"))}</label>
            <input type="number" className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500" value={this.state.file.tokenCount} onChange={e => this.updateFileField("tokenCount", e.target.value)} />
          </div>
          <div className="flex flex-col sm:flex-row gap-2">
            <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(i18next.t("general:Status"), i18next.t("general:Status - Tooltip"))}</label>
            <select className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={this.state.file.status} onChange={e => this.updateFileField("status", e.target.value)}>
              <option value="Pending">{i18next.t("application:Pending")}</option>
              <option value="Processing">{i18next.t("file:Processing")}</option>
              <option value="Finished">{i18next.t("file:Finished")}</option>
              <option value="Error">{i18next.t("general:Error")}</option>
            </select>
          </div>
          <div className="flex flex-col sm:flex-row gap-2">
            <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(i18next.t("message:Error text"), i18next.t("message:Error text - Tooltip"))}</label>
            <textarea className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 min-h-[60px]" value={this.state.file.errorText} readOnly rows={3} />
          </div>
        </div>
      </div>
    );
  }

  submitFileEdit(exitAfterSave) {
    const file = Setting.deepCopy(this.state.file);
    FileBackend.updateFile(this.state.file.owner, this.state.fileName, file)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({fileName: this.state.file.name, isNewFile: false});
            if (exitAfterSave) {
              this.props.history.push("/files");
            } else {
              this.props.history.push(`/files/${encodeURIComponent(this.state.file.name)}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateFileField("name", this.state.fileName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  cancelFileEdit() {
    if (this.state.isNewFile) {
      FileBackend.deleteFile(this.state.file)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/files");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/files");
    }
  }

  render() {
    return (
      <div>
        {this.state.file !== null ? this.renderFile() : null}
        <div className="mt-5 ml-10 flex gap-3">
          <button onClick={() => this.submitFileEdit(false)} className="px-6 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Save")}</button>
          <button onClick={() => this.submitFileEdit(true)} className="px-6 py-2 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewFile && <button onClick={() => this.cancelFileEdit()} className="px-6 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
}

export default FileEditPage;
