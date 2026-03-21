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
import i18next from "i18next";
import * as Setting from "./Setting";
import * as VectorBackend from "./backend/VectorBackend";
import Editor from "./common/Editor";

class VectorEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      vectorName: props.match.params.vectorName,
      vector: null,
      isNewVector: props.location?.state?.isNewVector || false,
      mode: new URLSearchParams(props.location?.search || "").get("mode") || "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getVector();
  }

  getVector() {
    VectorBackend.getVector("admin", this.props.match.params.vectorName)
      .then((res) => {
        if (res.data === null) {
          this.props.history.push("/404");
          return;
        }
        if (res.status === "ok") {
          this.setState({vector: res.data});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseVectorField(key, value) {
    if (key === "data") {
      value = value.split(",").map(Number);
    }
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateVectorField(key, value) {
    value = this.parseVectorField(key, value);
    const vector = this.state.vector;
    vector[key] = value;
    this.setState({vector: vector});
  }

  renderFormRow(label, tooltip, content) {
    return (
      <div className="flex items-start gap-4 mt-5">
        <label className="w-40 shrink-0 pt-1 text-sm text-muted-foreground text-right">
          {Setting.getLabel(label, tooltip)} :
        </label>
        <div className="flex-1">{content}</div>
      </div>
    );
  }

  renderVector() {
    const isViewMode = this.state.mode === "view";
    return (
      <div className="rounded-lg border border-border bg-card p-6 ml-1">
        <div className="flex items-center gap-4 mb-6">
          <h2 className="text-lg font-semibold text-foreground">{isViewMode ? i18next.t("vector:View Vector") : i18next.t("vector:Edit Vector")}</h2>
          {!isViewMode && (
            <>
              <button onClick={() => this.submitVectorEdit(false)} className="rounded-md border border-border px-3 py-1.5 text-sm text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Save")}</button>
              <button onClick={() => this.submitVectorEdit(true)} className="rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Save & Exit")}</button>
              {this.state.isNewVector && <button onClick={() => this.cancelVectorEdit()} className="rounded-md border border-border px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Cancel")}</button>}
            </>
          )}
        </div>

        {this.renderFormRow(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50" value={this.state.vector.name} disabled={isViewMode} onChange={e => this.updateVectorField("name", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50" value={this.state.vector.displayName} disabled={isViewMode} onChange={e => this.updateVectorField("displayName", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("general:Store"), i18next.t("general:Store - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50" value={this.state.vector.store} disabled={isViewMode} onChange={e => this.updateVectorField("store", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("general:Provider"), i18next.t("general:Provider - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50" value={this.state.vector.provider} disabled={isViewMode} onChange={e => this.updateVectorField("provider", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("store:File"), i18next.t("store:File - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50" value={this.state.vector.file} disabled={isViewMode} onChange={e => this.updateVectorField("file", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("general:Text"), i18next.t("general:Text - Tooltip"),
          <Editor
            value={this.state.vector.text}
            lang="markdown"
            dark
            fillHeight
            fillWidth
            readOnly={isViewMode}
            onChange={value => {
              if (!isViewMode) {
                this.updateVectorField("text", value);
              }
            }}
          />
        )}
        {this.renderFormRow(i18next.t("general:Size"), i18next.t("general:Size - Tooltip"),
          <input type="number" className="w-32 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground opacity-50 cursor-not-allowed" value={this.state.vector.size} disabled />
        )}
        {this.renderFormRow(i18next.t("vector:Dimension"), i18next.t("vector:Dimension - Tooltip"),
          <input type="number" className="w-32 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground opacity-50 cursor-not-allowed" value={this.state.vector.dimension} disabled />
        )}
        {this.renderFormRow(i18next.t("general:Data"), i18next.t("general:Data - Tooltip"),
          <textarea className="w-full min-h-[2.5rem] max-h-[22rem] resize-y rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50" value={this.state.vector.data} disabled={isViewMode} onChange={e => this.updateVectorField("data", e.target.value)} rows={1} />
        )}
      </div>
    );
  }

  submitVectorEdit(exitAfterSave) {
    const vector = Setting.deepCopy(this.state.vector);
    VectorBackend.updateVector(this.state.vector.owner, this.state.vectorName, vector)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({vectorName: this.state.vector.name, isNewVector: false});
            if (exitAfterSave) {
              this.props.history.push("/vectors");
            } else {
              this.props.history.push(`/vectors/${this.state.vector.name}`);
              this.getVector();
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateVectorField("name", this.state.vectorName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  cancelVectorEdit() {
    if (this.state.isNewVector) {
      VectorBackend.deleteVector(this.state.vector)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/vectors");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/vectors");
    }
  }

  render() {
    const isViewMode = this.state.mode === "view";
    return (
      <div>
        {this.state.vector !== null ? this.renderVector() : null}
        {!isViewMode && (
          <div className="mt-5 ml-10 flex gap-4">
            <button onClick={() => this.submitVectorEdit(false)} className="rounded-md border border-border px-5 py-2 text-sm text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Save")}</button>
            <button onClick={() => this.submitVectorEdit(true)} className="rounded-md bg-primary px-5 py-2 text-sm text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Save & Exit")}</button>
            {this.state.isNewVector && <button onClick={() => this.cancelVectorEdit()} className="rounded-md border border-border px-5 py-2 text-sm text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Cancel")}</button>}
          </div>
        )}
      </div>
    );
  }
}

export default VectorEditPage;
