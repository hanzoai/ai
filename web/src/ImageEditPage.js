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
import * as ImageBackend from "./backend/ImageBackend";
import * as Setting from "./Setting";
import i18next from "i18next";

class ImageEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      imageOwner: props.match.params.organizationName,
      imageName: props.match.params.imageName,
      image: null,
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getImage();
  }

  getImage() {
    ImageBackend.getImage(this.props.account.owner, this.state.imageName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({image: res.data});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseImageField(key, value) {
    if ([].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateImageField(key, value) {
    value = this.parseImageField(key, value);
    const image = this.state.image;
    image[key] = value;
    this.setState({image: image});
  }

  renderImage() {
    const fields = [
      {key: "owner", label: i18next.t("general:Organization"), tooltip: i18next.t("general:Organization - Tooltip")},
      {key: "name", label: i18next.t("general:Name"), tooltip: i18next.t("general:Name - Tooltip")},
      {key: "category", label: i18next.t("general:Category"), tooltip: i18next.t("provider:Category - Tooltip"), type: "select", options: [
        {value: "Private Image", label: "Private Image"},
        {value: "Public Image", label: "Public Image"},
        {value: "Market Image", label: "Market Image"},
        {value: "Community Image", label: "Community Image"},
      ]},
      {key: "imageId", label: i18next.t("general:ID"), tooltip: i18next.t("general:ID - Tooltip")},
      {key: "state", label: i18next.t("general:State"), tooltip: i18next.t("general:State - Tooltip")},
      {key: "tag", label: i18next.t("general:Tag"), tooltip: i18next.t("general:Tag - Tooltip")},
      {key: "description", label: i18next.t("general:Description"), tooltip: i18next.t("general:Description - Tooltip"), type: "textarea"},
      {key: "os", label: i18next.t("node:OS"), tooltip: i18next.t("node:OS - Tooltip")},
      {key: "platform", label: i18next.t("image:Platform"), tooltip: i18next.t("image:Platform - Tooltip")},
      {key: "systemArchitecture", label: i18next.t("image:Arch"), tooltip: i18next.t("image:Arch - Tooltip")},
      {key: "size", label: i18next.t("general:Size"), tooltip: i18next.t("general:Size - Tooltip")},
      {key: "bootMode", label: i18next.t("image:Boot mode"), tooltip: i18next.t("image:Boot mode - Tooltip")},
      {key: "progress", label: i18next.t("general:Progress"), tooltip: i18next.t("general:Progress - Tooltip")},
    ];

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg">
        <div className="flex items-center justify-between px-6 py-4 border-b border-zinc-800">
          <h3 className="text-base font-medium text-white">
            {this.state.mode === "add" ? i18next.t("image:New Image") : i18next.t("image:Edit Image")}
          </h3>
          <div className="flex gap-2">
            <button onClick={() => this.submitImageEdit(false)} className="px-4 py-1.5 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Save")}</button>
            <button onClick={() => this.submitImageEdit(true)} className="px-4 py-1.5 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Save & Exit")}</button>
            {this.state.mode === "add" && <button onClick={() => this.deleteImage()} className="px-4 py-1.5 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Cancel")}</button>}
          </div>
        </div>
        <div className="p-6 space-y-5">
          {fields.map(field => (
            <div key={field.key} className="flex flex-col sm:flex-row gap-2">
              <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{Setting.getLabel(field.label, field.tooltip)}</label>
              {field.type === "select" ? (
                <select className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={this.state.image[field.key]} onChange={e => this.updateImageField(field.key, e.target.value)}>
                  {field.options.map(opt => <option key={opt.value} value={opt.value}>{opt.label}</option>)}
                </select>
              ) : field.type === "textarea" ? (
                <textarea className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500" value={this.state.image[field.key]} onChange={e => this.updateImageField(field.key, e.target.value)} rows={3} />
              ) : (
                <input className="flex-1 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500" value={this.state.image[field.key]} onChange={e => this.updateImageField(field.key, e.target.value)} />
              )}
            </div>
          ))}
        </div>
      </div>
    );
  }

  submitImageEdit(willExist) {
    const image = Setting.deepCopy(this.state.image);
    ImageBackend.updateImage(this.state.image.owner, this.state.imageName, image)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({imageName: this.state.image.name});
            if (willExist) {
              this.props.history.push("/images");
            } else {
              this.props.history.push(`/images/${this.state.image.owner}/${encodeURIComponent(this.state.image.name)}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
            this.updateImageField("name", this.state.imageName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  deleteImage() {
    ImageBackend.deleteImage(this.state.image)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/images");
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  render() {
    return (
      <div>
        {this.state.image !== null ? this.renderImage() : null}
        <div className="mt-5 ml-10 flex gap-3">
          <button onClick={() => this.submitImageEdit(false)} className="px-6 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Save")}</button>
          <button onClick={() => this.submitImageEdit(true)} className="px-6 py-2 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" && <button onClick={() => this.deleteImage()} className="px-6 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
}

export default ImageEditPage;
