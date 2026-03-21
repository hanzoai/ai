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

const ANALYZE_PROGRESS_DURATION_SEC = 300;
const ANALYZE_PROGRESS_TICK_MS = 500;
const ANALYZE_PROGRESS_MAX_PERCENT = 99;

import * as TaskBackend from "./backend/TaskBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import * as ProviderBackend from "./backend/ProviderBackend";
import * as MessageBackend from "./backend/MessageBackend";
import Editor from "./common/Editor";
import TaskAnalysisReport from "./TaskAnalysisReport";
import * as Provider from "./Provider";
import {Loader2} from "lucide-react";


class TaskEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      owner: props.match.params.owner,
      taskName: props.match.params.taskName,
      isNewTask: props.location?.state?.isNewTask || false,
      modelProviders: [],
      templates: [],
      task: null,
      analyzing: false,
      analyzeProgress: 0,
      loading: false,
      uploadingDocument: false,
    };
    this.analyzeProgressIntervalId = null;
    this.analyzeStartTime = null;
  }

  componentWillUnmount() {
    if (this.analyzeProgressIntervalId !== null) {
      clearInterval(this.analyzeProgressIntervalId);
    }
  }

  UNSAFE_componentWillMount() {
    this.getTask();
    this.getModelProviders();
    if (Setting.isAdminUser(this.props.account)) {
      TaskBackend.getTaskTemplates().then((res) => {
        if (res.status === "ok" && res.data) {
          this.setState({templates: res.data});
        }
      });
    }
  }

  normalizeTaskResult(task) {
    if (!task || !task.result) {
      return task;
    }
    if (typeof task.result === "string") {
      try {
        task = {...task, result: JSON.parse(task.result)};
      } catch {
        task = {...task, result: null};
      }
    }
    return task;
  }

  getTask() {
    TaskBackend.getTask(this.state.owner, this.state.taskName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            task: this.normalizeTaskResult(res.data),
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getEffectiveScale() {
    if (this.state.task.template) {
      const tpl = this.state.templates.find((t) => `${t.owner}/${t.name}` === this.state.task.template);
      return tpl ? (tpl.scale || "") : "";
    }
    return this.state.task.scale || "";
  }

  getQuestion() {
    const scale = this.getEffectiveScale();
    return `${scale.replace("{example}", this.state.task.example).replace("{labels}", this.state.task.labels.map(label => `"${label}"`).join(", "))}`;
  }

  analyzeTask() {
    this.analyzeStartTime = Date.now();
    this.setState({analyzing: true, analyzeProgress: 0});
    const durationMs = ANALYZE_PROGRESS_DURATION_SEC * 1000;
    this.analyzeProgressIntervalId = setInterval(() => {
      const elapsed = Date.now() - this.analyzeStartTime;
      const percent = Math.min(ANALYZE_PROGRESS_MAX_PERCENT, (99 * elapsed) / durationMs);
      this.setState({analyzeProgress: Math.round(percent)});
    }, ANALYZE_PROGRESS_TICK_MS);
    TaskBackend.analyzeTask(this.state.task.owner, this.state.task.name)
      .then((res) => {
        if (res.status === "ok") {
          const task = this.state.task;
          task.result = res.data;
          task.score = res.data.score;
          this.setState({task: task});
          Setting.showMessage("success", i18next.t("general:Successfully saved"));
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      })
      .catch(err => {
        Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${err.message}`);
      })
      .finally(() => {
        if (this.analyzeProgressIntervalId !== null) {
          clearInterval(this.analyzeProgressIntervalId);
          this.analyzeProgressIntervalId = null;
        }
        this.setState({analyzeProgress: 100}, () => {
          setTimeout(() => {
            this.setState({analyzing: false, analyzeProgress: 0});
          }, 400);
        });
      });
  }

  clearReport = () => {
    const task = this.state.task;
    task.result = null;
    task.score = 0;
    this.setState({task: task});
  };

  getAnswer() {
    const provider = this.state.task.provider;
    const question = this.getQuestion();
    const framework = this.state.task.name;
    const video = "";
    MessageBackend.getAnswer(provider, question, framework, video)
      .then((res) => {
        if (res.status === "ok") {
          this.updateTaskField("log", res.data);
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }

        this.setState({
          loading: false,
        });
      });
  }

  getModelProviders() {
    ProviderBackend.getProviders(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            modelProviders: res.data.filter(provider => provider.category === "Model"),
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseTaskField(key, value) {
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateTaskField(key, value) {
    value = this.parseTaskField(key, value);

    const task = this.state.task;
    task[key] = value;
    this.setState({
      task: task,
    });
  }

  fileToBase64 = (file) => {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.readAsDataURL(file);
      reader.onload = () => resolve(reader.result);
      reader.onerror = error => reject(error);
    });
  };

  handleDocumentUpload = async({file}) => {
    this.setState({uploadingDocument: true});

    const base64Data = await this.fileToBase64(file);
    const taskId = `${this.state.task.owner}/${this.state.task.name}`;

    TaskBackend.uploadTaskDocument(taskId, base64Data, file.name, file.type)
      .then((res) => {
        if (res.status === "ok") {
          const result = res.data;
          // Update both fields in a single setState to avoid race conditions
          const task = this.state.task;
          task.documentUrl = result.url;
          task.documentText = result.text;
          this.setState({task: task});

          Setting.showMessage("success", i18next.t("general:Successfully uploaded"));
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to upload")}: ${res.msg}`);
        }
      })
      .catch(err => {
        Setting.showMessage("error", `${i18next.t("general:Failed to upload")}: ${err.message}`);
      })
      .finally(() => {
        this.setState({uploadingDocument: false});
      });
  };

  clearDocument = () => {
    const task = this.state.task;
    task.documentUrl = "";
    task.documentText = "";
    this.setState({task: task});
  };

  getDocumentFileName() {
    const url = this.state.task?.documentUrl || "";
    try {
      const path = new URL(url).pathname || url;
      const encoded = path.split("/").filter(Boolean).pop() || url;
      try {
        return decodeURIComponent(encoded);
      } catch {
        return encoded;
      }
    } catch {
      return url;
    }
  }

  renderTask() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {i18next.t("task:Edit Task")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitTaskEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitTaskEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewTask && <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelTaskEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            <div>{Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :</div>
            <Input value={this.state.task.name} onChange={e => {
              this.updateTaskField("name", e.target.value);
            }} />
          </div>
          {Setting.isAdminUser(this.props.account) ? (
            <>
              <div className="flex-1">
                <div>{Setting.getLabel(i18next.t("provider:Model provider"), i18next.t("provider:Model provider - Tooltip"))} :</div>
                <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.task.provider}> this.updateTaskField("provider", value)}
                  options={this.state.modelProviders.map((p) => ({
                    value: p.name,
                    label: (
                      <span style={{display: "inline-flex", alignItems: "center", gap: 8}}>
                        <Provider.ProviderLogo provider={p} width={20} height={20} />
                        <span>{p.displayName} ({p.name})</span>
                      </span>
                    ),
                  }))}
                />
              </div>
              <div className="flex-1">
                <div>{Setting.getLabel(i18next.t("general:Type"), i18next.t("general:Type - Tooltip"))} :</div>
                <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.task.type}> {this.updateTaskField("type", value);})}>
                  {
                    [
                      {id: "Labeling", name: "Labeling"},
                      {id: "PBL", name: "PBL"},
                    ].map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
                  }
                </select>
              </div>
            </>
          ) : null}
        </div>
        {
          this.state.task.type !== "Labeling" ? null : (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
              </div>
              <div className="flex-1">
                <Input value={this.state.task.displayName} onChange={e => {
                  this.updateTaskField("displayName", e.target.value);
                }} />
              </div>
            </div>
          )
        }
        {
          Setting.isAdminUser(this.props.account) ? (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("general:Template"), i18next.t("general:Template - Tooltip"))} :
              </div>
              <div className="flex-1">
                <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.task.template ?? ""}> this.updateTaskField("template", value || "")}
                  options={[
                    {value: "", label: i18next.t("general:None")},
                    ...this.state.templates.map((t) => ({value: `${t.owner}/${t.name}`, label: t.displayName ? `${t.displayName} (${t.owner}/${t.name})` : `${t.owner}/${t.name}`})),
                  ]}
                />
              </div>
            </div>
          ) : null
        }
        {
          Setting.isAdminUser(this.props.account) ? (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("task:Scale"), i18next.t("task:Scale - Tooltip"))} :
              </div>
              <div className="flex-1">
                <span className="text-zinc-300 text-sm"> this.updateTaskField("scale", e.target.value)}
                />
              </div>
            </div>
          ) : null
        }
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("store:File"), i18next.t("store:File - Tooltip"))} :
          </div>
          <div className="flex-1">
            {this.state.task.documentUrl ? (
              <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
                <Space align="center">
                  <span style={{fontSize: 28, color: this.state.task.documentUrl.endsWith(".pdf") ? "#cf1322" : "#1890ff"}}>
                    {this.state.task.documentUrl.endsWith(".pdf") ?  : }
                  </span>
                  <Typography.Text ellipsis style={{maxWidth: 420}}>{this.getDocumentFileName()}</Typography.Text>
                  <button className="px-2 py-1 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} href={this.state.task.documentUrl} target="_blank" rel="noopener noreferrer">
                    {i18next.t("general:Download")}
                  </button>
                  <button className="px-2 py-1 rounded text-xs font-medium transition-colors bg-red-600 text-white hover:bg-red-700">} onClick={this.clearDocument} aria-label={i18next.t("general:Delete")} />
                </Space>
              </div>
            ) : (
              
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200">} loading={this.state.uploadingDocument}>
                  {i18next.t("store:Upload file")} (.docx, .pdf)
                </button>
              
            )}
          </div>
        </div>
        {
          (this.state.task.type !== "Labeling") ? null : (
            <React.Fragment>
              <div className="flex flex-col sm:flex-row gap-2 mt-4">
                <div className="flex-1">
                  {Setting.getLabel(i18next.t("task:Example"), i18next.t("task:Example - Tooltip"))} :
                </div>
                <div className="flex-1">
                  <Input value={this.state.task.example} onChange={e => {
                    this.updateTaskField("example", e.target.value);
                  }} />
                </div>
              </div>
              <div className="flex flex-col sm:flex-row gap-2 mt-4">
                <div className="flex-1">
                  {Setting.getLabel(i18next.t("task:Labels"), i18next.t("task:Labels - Tooltip"))} :
                </div>
                <div className="flex-1">
                  <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.task.labels}> {this.updateTaskField("labels", value);})}>
                    {
                      this.state.task.labels?.map((item, index) => <option key={index} value={item}>{item}</option>)
                    }
                  </select>
                </div>
              </div>
            </React.Fragment>
          )
        }
        {
          (this.state.task.type !== "Labeling") && this.state.task.documentUrl ? (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("task:Report"), i18next.t("task:Report - Tooltip"))} :
              </div>
              <div className="flex-1">
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200 disabled:opacity-50" disabled={!this.state.task.documentText || !!this.state.task.result} style={{marginBottom: "20px", width: "200px"}> this.analyzeTask()}
                >
                  {i18next.t("task:Analyze")}
                </button>
                {Setting.isAdminUser(this.props.account) && this.state.task.result ? (
                  <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" onClick={this.clearReport} style={{marginBottom: "20px", marginLeft: "8px", width: "200px"}>
                    {i18next.t("general:Clear")}
                  </button>
                ) : null}
                {this.state.analyzing && (
                  <>
                    <div style={{maxWidth: "400px", marginTop: "8px", marginBottom: "8px"}}>
                      <Progress percent={this.state.analyzeProgress} status="active" />
                    </div>
                    <div className="flex justify-center py-8"><div className="w-8 h-8 border-2 border-zinc-700 border-t-white rounded-full animate-spin" /></div>
                  </>
                )}
                {this.state.task.result && <TaskAnalysisReport result={this.state.task.result} />}
              </div>
            </div>
          ) : this.state.task.type === "Labeling" ? (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("task:Log"), i18next.t("task:Log - Tooltip"))} :
              </div>
              <div className="flex-1">
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" onClick={this.runTask.bind(this)} style={{marginBottom: "20px", width: "100px"}>{i18next.t("general:Run")}</button>
                <div style={{height: "200px"}}>
                  <Editor
                    value={this.state.task.log}
                    lang="js"
                    fillHeight
                    dark
                    onChange={value => {
                      this.updateTaskField("log", value);
                    }}
                  />
                </div>
              </div>
            </div>
          ) : null
        }
      </div>
    );
  }

  runTask() {
    this.updateTaskField("log", "");
    this.setState({
      loading: true,
    });
    this.getAnswer();
  }

  submitTaskEdit(exitAfterSave) {
    const task = Setting.deepCopy(this.state.task);
    if (task.result && typeof task.result === "object") {
      task.result = JSON.stringify(task.result);
    }
    TaskBackend.updateTask(this.state.task.owner, this.state.taskName, task)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              taskName: this.state.task.name,
              isNewTask: false,
            });
            if (exitAfterSave) {
              this.props.history.push("/tasks");
            } else {
              this.props.history.push(`/tasks/${this.state.task.owner}/${this.state.task.name}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateTaskField("name", this.state.taskName);
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
          this.state.task !== null ? this.renderTask() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitTaskEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitTaskEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewTask && <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelTaskEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
  cancelTaskEdit() {
    if (this.state.isNewTask) {
      TaskBackend.deleteTask(this.state.task)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/tasks");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/tasks");
    }
  }

}

export default TaskEditPage;
