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
import * as TemplateBackend from "./backend/TemplateBackend";
import * as StoreBackend from "./backend/StoreBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import StoreAvatarUploader from "./AvatarUpload";
import Editor from "./common/Editor";
import TextArea from "antd/es/input/TextArea";
import TemplateOptionTable from "./table/TemplateOptionTable";

class TemplateEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      templateName: props.match.params.templateName,
      isNewTemplate: props.location?.state?.isNewTemplate || false,
      template: null,
      defaultStore: null,
    };
  }

  UNSAFE_componentWillMount() {
    this.getTemplate();
    this.getDefaultStore();
  }

  getTemplate() {
    TemplateBackend.getTemplate(this.props.account.name, this.state.templateName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            template: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getDefaultStore() {
    StoreBackend.getStores(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          const defaultStore = res.data.find(store => store.isDefault);
          if (defaultStore) {
            this.setState({
              defaultStore: defaultStore,
            });
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseTemplateField(key, value) {
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateTemplateField(key, value) {
    value = this.parseTemplateField(key, value);

    const template = this.state.template;
    template[key] = value;
    this.setState({
      template: template,
    });
  }

  renderTemplate() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {i18next.t("template:Edit Template")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitTemplateEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitTemplateEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewTemplate && <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelTemplateEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.template.name} onChange={e => {
              this.updateTemplateField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.template.displayName} onChange={e => {
              this.updateTemplateField("displayName", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Description"), i18next.t("general:Description - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="text-zinc-300 text-sm"> {
              this.updateTemplateField("description", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("template:Readme"), i18next.t("template:Readme - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.template.readme} onChange={e => {
              this.updateTemplateField("readme", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Version"), i18next.t("general:Version - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.template.version} onChange={e => {
              this.updateTemplateField("version", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Icon"), i18next.t("general:Icon - Tooltip"))} :
          </div>
          <div className="flex-1">
            <StoreAvatarUploader
              store={this.state.defaultStore}
              imageUrl={this.state.template.icon}
              onUpdate={(newUrl) => {
                this.updateTemplateField("icon", newUrl);
              }}
              onUploadComplete={(newUrl) => {
                this.submitTemplateEdit(false);
              }}
            />
          </div>
        </div>

        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("template:Enable basic config"), i18next.t("template:Enable basic config - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="px-2 py-0.5 rounded text-xs " + (this.state.template.enableBasicConfig ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{this.state.template.enableBasicConfig ? "ON" : "OFF"}</span>
          </div>
        </div>

        {this.state.template.enableBasicConfig && (
          <div className="flex flex-col sm:flex-row gap-2 mt-4">
            <div className="flex-1">
              {Setting.getLabel(i18next.t("template:Basic config"), i18next.t("template:Basic config - Tooltip"))} :
            </div>
            <div className="flex-1">
              <TemplateOptionTable
                mode="edit"
                templateOptions={this.state.template.basicConfigOptions}
                onUpdateTemplateOptions={options => {this.updateTemplateField("basicConfigOptions", options);}} />
            </div>
          </div>
        )}

        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("template:Manifest"), i18next.t("template:Manifest - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div style={{height: "500px"}}>
              <Editor
                value={this.state.template.manifest}
                lang="yaml"
                fillHeight
                dark
                onChange={value => {
                  this.updateTemplateField("manifest", value);
                }}
              />
            </div>
          </div>
        </div>
      </div>
    );
  }

  submitTemplateEdit(exitAfterSave) {
    const template = Setting.deepCopy(this.state.template);
    TemplateBackend.updateTemplate(this.state.template.owner, this.state.templateName, template)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              templateName: this.state.template.name,
              isNewTemplate: false,
            });

            if (exitAfterSave) {
              this.props.history.push("/templates");
            } else {
              this.props.history.push(`/templates/${this.state.template.name}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateTemplateField("name", this.state.templateName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  render() {
    return (
      <div>
        {
          this.state.template !== null ? this.renderTemplate() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitTemplateEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitTemplateEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewTemplate && <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelTemplateEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
  cancelTemplateEdit() {
    if (this.state.isNewTemplate) {
      TemplateBackend.deleteTemplate(this.state.template)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/templates");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/templates");
    }
  }

}

export default TemplateEditPage;
