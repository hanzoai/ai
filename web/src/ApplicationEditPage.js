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
import * as ApplicationBackend from "./backend/ApplicationBackend";
import * as TemplateBackend from "./backend/TemplateBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import TemplateOptionTable from "./table/TemplateOptionTable";
import Editor from "./common/Editor";


class ApplicationEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      applicationName: props.match.params.applicationName,
      application: null,
      templates: [],
      deploying: false,
      isNewApplication: props.location?.state?.isNewApplication || false,
    };
  }

  UNSAFE_componentWillMount() {
    this.getApplication();
    this.getTemplates();
  }

  getApplication() {
    ApplicationBackend.getApplication(this.props.account.name, this.state.applicationName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            application: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  checkBasicConfigOptions() {
    const template = this.state.templates.find(template => template.name === this.state.application.template);
    if (template && template.basicConfigOptions) {
      for (const option of template.basicConfigOptions) {
        const setting = this.state.application.basicConfigOptions.find(o => o.parameter === option.parameter)?.setting;
        if (option.required && (!setting || setting === "")) {
          Setting.showMessage("error", `${i18next.t("general:Missing required parameter in basic config")}: ${option.parameter}`);
          return false;
        }
      }
    }
    return true;
  }

  deployApplication() {
    if (!this.checkBasicConfigOptions()) {
      return;
    }
    this.setState({deploying: true});

    ApplicationBackend.deployApplication(this.state.application)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deployed"));
          this.setState({
            application: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to deploy")}: ${res.msg}`);
        }
        this.setState({deploying: false});
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to deploy")}: ${error}`);
        this.setState({deploying: false});
      });
  }

  undeployApplication() {
    this.setState({deploying: true});

    ApplicationBackend.undeployApplication(this.state.application.owner, this.state.application.name)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully undeployed"));
          this.getApplication();
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to undeploy")}: ${res.msg}`);
        }
        this.setState({deploying: false});
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to undeploy")}: ${error}`);
        this.setState({deploying: false});
      });
  }

  getTemplates() {
    TemplateBackend.getTemplates(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            templates: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseApplicationField(key, value) {
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateApplicationField(key, value) {
    value = this.parseApplicationField(key, value);

    const application = this.state.application;
    application[key] = value;
    this.setState({
      application: application,
    });
  }

  renderApplication() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {i18next.t("application:Edit Application")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitApplicationEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitApplicationEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewApplication && <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelApplicationEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      } style={(Setting.isMobile()) ? {margin: "5px"} : {}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.application.name} onChange={e => {
              this.updateApplicationField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.application.displayName} onChange={e => {
              this.updateApplicationField("displayName", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Description"), i18next.t("general:Description - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="text-zinc-300 text-sm"> {
              this.updateApplicationField("description", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Template"), i18next.t("general:Template - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.application.template}> {
                this.setState({template: this.state.templates.find((template) => template.name === value)});
                this.updateApplicationField("template", value);
                this.updateApplicationField("basicConfigOptions", this.state.templates.find((template) => template.name === value)?.basicConfigOptions?.map(option => ({
                  parameter: option.parameter,
                  setting: option.default,
                })) || []);
              })}
              options={this.state.templates.map((template) => Setting.getOption(`${template.displayName} (${template.name})`, `${template.name}`))
              } />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Status"), i18next.t("general:Status - Tooltip"))} :
          </div>
          <div className="flex-1">
            {Setting.getApplicationStatusTag(this.state.application.status)}
            {
              this.state.application.status === "Not Deployed" ? (
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "10px"}> this.deployApplication()}>
                  {i18next.t("application:Deploy")}
                </button>
              ) : (
                this.undeployApplication()} okText={i18next.t("general:OK")} cancelText={i18next.t("general:Cancel")}>
                  <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-red-600 text-white hover:bg-red-700" style={{marginLeft: "10px"}>
                    {i18next.t("application:Undeploy")}
                  </button>
              )
            }
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Namespace"), i18next.t("general:Namespace - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input disabled value={this.state.application.namespace} onChange={e => {
              this.updateApplicationField("namespace", e.target.value);
            }} />
          </div>
        </div>

        {this.state.templates && this.state.templates.find(template => template.name === this.state.application.template && template.enableBasicConfig) ? (
          <>
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("template:Basic config"), i18next.t("template:Basic config - Tooltip"))} :
              </div>
              <div className="flex-1">
                <TemplateOptionTable
                  templateOptions={this.state.templates.find(template => template.name === this.state.application.template)?.basicConfigOptions || []}
                  options={this.state.application.basicConfigOptions}
                  onUpdateOptions={options => {this.updateApplicationField("basicConfigOptions", options);}}
                />
              </div>
            </div>
          </>
        ) : null}

        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("application:Parameters"), i18next.t("application:Parameters - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div style={{height: "500px"}}>
              <Editor
                value={this.state.application.parameters}
                lang="yaml"
                fillHeight
                dark
                onChange={value => {
                  this.updateApplicationField("parameters", value);
                }}
              />
            </div>
          </div>
        </div>

        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("application:Deployment manifest"), i18next.t("application:Deployment manifest - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div style={{height: "500px"}}>
              <Editor
                value={this.state.application.manifest}
                lang="yaml"
                fillHeight
                dark
                readOnly
              />
            </div>
          </div>
        </div>
      </div>
    );
  }

  submitApplicationEdit(exitAfterSave) {
    if (!this.checkBasicConfigOptions()) {
      return;
    }

    const application = Setting.deepCopy(this.state.application);
    ApplicationBackend.updateApplication(this.state.application.owner, this.state.applicationName, application)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              applicationName: this.state.application.name,
              isNewApplication: false,
            });

            if (exitAfterSave) {
              this.props.history.push("/applications");
            } else {
              this.props.history.push(`/applications/${this.state.application.name}`);
              this.getApplication();
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateApplicationField("name", this.state.applicationName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  cancelApplicationEdit() {
    if (this.state.isNewApplication) {
      ApplicationBackend.deleteApplication(this.state.application)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/applications");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/applications");
    }
  }

  render() {
    return (
      <div>
        {
          this.state.application !== null ? this.renderApplication() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitApplicationEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitApplicationEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewApplication && <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelApplicationEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
}

export default ApplicationEditPage;
