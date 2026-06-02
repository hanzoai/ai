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
import * as FormBackend from "./backend/FormBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import FormItemTable from "./table/FormItemTable";
import RecordListPage from "./RecordListPage";
import StoreListPage from "./StoreListPage";
import VectorListPage from "./VectorListPage";
import VideoListPage from "./VideoListPage";
import TaskListPage from "./TaskListPage";
import WorkflowListPage from "./WorkflowListPage";
import ArticleListPage from "./ArticleListPage";
import GraphListPage from "./GraphListPage";

const formTypeOptions = Setting.getFormTypeOptions();

class FormEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      formName: props.match.params.formName,
      isNewForm: props.location?.state?.isNewForm || false,
      form: null,
      formCount: "key",
    };
  }

  UNSAFE_componentWillMount() {
    this.getForm();
  }

  getForm() {
    FormBackend.getForm(this.props.account.owner, this.state.formName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            form: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseFormField(key, value) {
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateFormField(key, value) {
    value = this.parseFormField(key, value);

    const form = this.state.form;
    form[key] = value;
    this.setState({
      form: form,
    });
  }

  renderForm() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {i18next.t("form:Edit Form")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitFormEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitFormEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewForm && <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelFormEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.form.name} onChange={e => {
              this.updateFormField("name", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.form.displayName} onChange={e => {
              this.updateFormField("displayName", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("form:Position"), i18next.t("form:Position - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.form.position} onChange={e => {
              this.updateFormField("position", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Category"), i18next.t("provider:Category - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.form.category}> {
              this.updateFormField("category", value);
            })}>
              {
                [
                  {id: "Table", name: i18next.t("form:Table")},
                  {id: "iFrame", name: i18next.t("form:iFrame")},
                  {id: "List Page", name: i18next.t("form:List Page")},
                ].map((item, index) => <option key={index} value={item.id}>{item.name}</option>)
              }
            </select>
          </div>
        </div>
        {
          this.state.form.category === "Table" && (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("form:Form items"), i18next.t("form:Form items - Tooltip"))} :
              </div>
              <div className="flex-1">
                <FormItemTable
                  title={i18next.t("form:Form items")}
                  table={this.state.form.formItems}
                  category={this.state.form.category}
                  onUpdateTable={(value) => {this.updateFormField("formItems", value);}}
                />
              </div>
            </div>
          )
        }
        {
          this.state.form.category === "iFrame" && (
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("general:URL"), i18next.t("general:URL - Tooltip"))} :
              </div>
              <div className="flex-1">
                <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" />} value={this.state.form.url} onChange={e => {this.updateFormField("url", e.target.value);}} />
              </div>
            </div>
          )
        }
        {
          this.state.form.category === "List Page" && (
            <div>
              <div className="flex flex-col sm:flex-row gap-2 mt-4">
                <div className="flex-1">
                  {Setting.getLabel(i18next.t("general:Type"), i18next.t("general:Type - Tooltip"))} :
                </div>
                <div className="flex-1">
                  <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.form.type}> {
                      this.updateFormField("type", value);
                      this.updateFormField("name", value);
                      this.updateFormField("displayName", value);
                      const defaultItems = new FormItemTable({formType: value}).getItems();
                      this.updateFormField("formItems", defaultItems);
                    }}
                  >
                    {formTypeOptions.map(option => (
                      <option key={option.id} value={option.id}>{i18next.t(option.name)}</option>
                    ))}
                  </select>
                </div>
              </div>
              <div className="flex flex-col sm:flex-row gap-2 mt-4">
                <div className="flex-1">
                  {Setting.getLabel(i18next.t("general:Tag"), i18next.t("general:Tag - Tooltip"))} :
                </div>
                <div className="flex-1">
                  <Input value={this.state.form.tag} onChange={e => {
                    this.updateFormField("tag", e.target.value);
                    this.updateFormField("name", e.target.value ? `${this.state.form.type}-tag-${e.target.value}` : this.state.form.type);
                  }} />
                </div>
              </div>
              <div className="flex flex-col sm:flex-row gap-2 mt-4">
                <div className="flex-1">
                  {Setting.getLabel(i18next.t("form:Form items"), i18next.t("form:Form items - Tooltip"))} :
                </div>
                <div className="flex-1">
                  <FormItemTable
                    title={i18next.t("form:Form items")}
                    table={this.state.form.formItems}
                    category={this.state.form.category}
                    onUpdateTable={(value) => {
                      this.updateFormField("formItems", value);
                    }}
                    formType={this.state.form.type}
                  />
                </div>
              </div>
            </div>
          )
        }
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {i18next.t("general:Preview")}:
          </div>
          <div className="flex-1">
            {this.state.form.category === "List Page" ? (this.renderListPagePreview()) :
              <div key={this.state.formCount}>
                <iframe id="formData" title={"formData"} src={`${location.href}/data`} width="100%" height="700px" frameBorder="no" style={{border: "1px solid #e0e0e0", borderRadius: "8px", boxShadow: "0 2px 8px rgba(0, 0, 0, 0.1)"}} />
              </div>
            }
          </div>
        </div>
      </div>
    );
  }

  renderListPageItems() {

  }

  renderListPagePreview() {
    let listPageComponent = null;

    if (this.state.form.type === "records") {
      listPageComponent = (<RecordListPage {...this.props} formItems={this.state.form.formItems} />);
    } else if (this.state.form.type === "stores") {
      listPageComponent = (<StoreListPage {...this.props} formItems={this.state.form.formItems} />);
    } else if (this.state.form.type === "vectors") {
      listPageComponent = (<VectorListPage {...this.props} formItems={this.state.form.formItems} />);
    } else if (this.state.form.type === "videos") {
      listPageComponent = (<VideoListPage {...this.props} formItems={this.state.form.formItems} />);
    } else if (this.state.form.type === "tasks") {
      listPageComponent = (<TaskListPage {...this.props} formItems={this.state.form.formItems} />);
    } else if (this.state.form.type === "workflows") {
      listPageComponent = (<WorkflowListPage {...this.props} formItems={this.state.form.formItems} />);
    } else if (this.state.form.type === "articles") {
      listPageComponent = (<ArticleListPage {...this.props} formItems={this.state.form.formItems} />);
    } else if (this.state.form.type === "graphs") {
      listPageComponent = (<GraphListPage {...this.props} formItems={this.state.form.formItems} />);
    }

    return (
      <div style={{position: "relative", border: "1px solid rgb(217,217,217)", height: "600px", cursor: "pointer"}} onClick={(e) => {Setting.openLink(`/${this.state.form.type}`);}}>
        <div style={{position: "relative", height: "100%", overflow: "auto"}}>
          <div style={{display: "inline-block", position: "relative", zIndex: 1, pointerEvents: "none"}}>
            {listPageComponent}
          </div>
        </div>
        <div style={{position: "absolute", top: 0, left: 0, right: 0, bottom: 0, zIndex: 10, background: "rgba(0,0,0,0.4)", pointerEvents: "none"}} />
      </div>
    );
  }

  submitFormEdit(exitAfterSave) {
    const form = Setting.deepCopy(this.state.form);
    if (!exitAfterSave) {
      this.setState({
        formCount: this.state.formCount + "a",
      });
    }
    FormBackend.updateForm(this.state.form.owner, this.state.formName, form)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              formName: this.state.form.name,
              isNewForm: false,
            });
            if (exitAfterSave) {
              this.props.history.push("/forms");
            } else {
              this.props.history.push(`/forms/${this.state.form.name}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateFormField("name", this.state.formName);
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
          this.state.form !== null ? this.renderForm() : null
        }
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitFormEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitFormEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewForm && <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelFormEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
  cancelFormEdit() {
    if (this.state.isNewForm) {
      FormBackend.deleteForm(this.state.form)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/forms");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/forms");
    }
  }

}

export default FormEditPage;
