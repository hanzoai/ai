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
import * as WorkflowBackend from "./backend/WorkflowBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import BpmnComponent from "./BpmnComponent";
import Editor from "./common/Editor";

import ChatWidget from "./common/ChatWidget";

class WorkflowEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      workflowName: props.match.params.workflowName,
      isNewWorkflow: props.location?.state?.isNewWorkflow || false,
      modelProviders: [],
      workflow: null,
      chatPageObj: null,
      loading: false,
    };

    this.questionTemplatesOptions = [
      {value: "{{text}}", label: i18next.t("general:Text")},
      {value: "{{text2}}", label: i18next.t("general:Text2")},
      {value: "{{message}}", label: i18next.t("general:Message")},
      {value: "{{language}}", label: i18next.t("general:Language")},
    ];
  }

  UNSAFE_componentWillMount() {
    this.getWorkflow();
  }

  getWorkflow() {
    WorkflowBackend.getWorkflow(this.props.account.name, this.state.workflowName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            workflow: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseWorkflowField(key, value) {
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateWorkflowField(key, value) {
    value = this.parseWorkflowField(key, value);

    const workflow = this.state.workflow;
    workflow[key] = value;
    this.setState({
      workflow: workflow,
    });
  }

  renderQuestionTemplate() {
    const questionTemplate = this.state.workflow.questionTemplate;

    if (!questionTemplate) {
      return "";
    }

    // Render the question template with variables replaced
    const renderedTemplate = questionTemplate.replace(/#\{\{(\w+)\}\}/g, (match, variableName) => {
      if (variableName === "language") {
        const lang = Setting.getLanguage();
        return (!lang || lang === "null") ? "en" : lang;
      }
      return this.state.workflow[variableName] || "";
    });

    return renderedTemplate;
  }

  renderWorkflow() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {i18next.t("workflow:Edit Workflow")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitWorkflowEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitWorkflowEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewWorkflow && <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelWorkflowEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.workflow.name} onChange={e => {
              this.updateWorkflowField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.workflow.displayName} onChange={e => {
              this.updateWorkflowField("displayName", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Text"), i18next.t("general:Text - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div style={{height: "500px"}}>
              <Editor
                value={this.state.workflow.text}
                lang="xml"
                fillHeight
                fillWidth
                dark
                onChange={value => {
                  this.updateWorkflowField("text", value);
                }}
              />
            </div>
          </div>
          <div className="flex-1">
          <div className="flex-1">
            <div>
              <BpmnComponent
                diagramXML={this.state.workflow.text}
                onLoading={(info) => {
                  Setting.showMessage("success", info);
                }}
                onError={(err) => {
                  Setting.showMessage("error", err);
                }}
                onXMLChange={(xml) => {
                  this.updateWorkflowField("text", xml);
                }}
              />
            </div>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Text2"), i18next.t("general:Text2 - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div style={{height: "500px"}}>
              <Editor
                value={this.state.workflow.text2}
                lang="xml"
                fillHeight
                fillWidth
                dark
                onChange={value => {
                  this.updateWorkflowField("text2", value);
                }}
              />
            </div>
          </div>
          <div className="flex-1">
          <div className="flex-1">
            <div>
              <BpmnComponent
                diagramXML={this.state.workflow.text2}
                onLoading={(info) => {
                  Setting.showMessage("success", info);
                }}
                onError={(err) => {
                  Setting.showMessage("error", err);
                }}
                onXMLChange={(xml) => {
                  this.updateWorkflowField("text2", xml);
                }}
              />
            </div>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Message"), i18next.t("general:Message - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div style={{height: "500px"}}>
              <Editor
                value={this.state.workflow.message}
                lang="html"
                fillHeight
                fillWidth
                dark
                onChange={value => {
                  this.updateWorkflowField("message", value);
                }}
              />
            </div>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Template"), i18next.t("general:Template - Tooltip"))} :
          </div>
          <div className="flex-1">
            <div className="flex-1">
                    <div style={{marginBottom: "8px"}}>
                      {i18next.t("general:Template")}:
                    </div>
                    <Mentions rows={25} prefix={"#"} options={this.questionTemplatesOptions} value={this.state.workflow.questionTemplate} onChange={(value) => this.updateWorkflowField("questionTemplate", value)} />
                  </div>
                  <div className="flex-1">
                    <div style={{marginBottom: "8px"}}>
                      {i18next.t("general:Preview")}:
                    </div>
                    <div style={{height: "600px", borderRadius: "4px"}}>
                      <Editor
                        value={this.renderQuestionTemplate()}
                        lang="markdown"
                        fillHeight
                        fillWidth
                        dark
                        readOnly
                      />
                    </div>
                  </div>
                </div>
              }>
              <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={Setting.getShortText(this.state.workflow.questionTemplate, 60)} readOnly />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Response"), i18next.t("general:Response - Tooltip"))} :
          </div>
          <div className="flex-1">
            <ChatWidget
              chatName={`workflow_chat_${this.state.workflowName}`}
              displayName={`${i18next.t("general:Chat")} - ${this.state.workflowName}`}
              category="Workflow"
              account={this.props.account}
              title={i18next.t("general:Chat")}
              height="800px"
              showNewChatButton={true}
              prompts={[{
                "title": i18next.t("task:Generate Project"),
                "text": this.renderQuestionTemplate(),
                "image": "",
              }]}
            />
          </div>
        </div>
      </div>
    );
  }

  submitWorkflowEdit(exitAfterSave) {
    const workflow = Setting.deepCopy(this.state.workflow);
    WorkflowBackend.updateWorkflow(this.state.workflow.owner, this.state.workflowName, workflow)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              workflowName: this.state.workflow.name,
              isNewWorkflow: false,
            });
            if (exitAfterSave) {
              this.props.history.push("/workflows");
            } else {
              this.props.history.push(`/workflows/${this.state.workflow.name}`);
              this.getWorkflow();
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateWorkflowField("name", this.state.workflowName);
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
          this.state.workflow !== null ? this.renderWorkflow() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitWorkflowEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitWorkflowEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewWorkflow && <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelWorkflowEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
  cancelWorkflowEdit() {
    if (this.state.isNewWorkflow) {
      WorkflowBackend.deleteWorkflow(this.state.workflow)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/workflows");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/workflows");
    }
  }

}

export default WorkflowEditPage;
