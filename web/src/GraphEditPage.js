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
import * as GraphBackend from "./backend/GraphBackend";
import * as ChatBackend from "./backend/ChatBackend";
import * as StoreBackend from "./backend/StoreBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import GraphDataPage from "./GraphDataPage";
import GraphChatDataPage from "./GraphChatDataPage";
import GraphChatTable from "./GraphChatTable";
import Editor from "./common/Editor";
import dayjs from "dayjs";
import weekday from "dayjs/plugin/weekday";
import localeData from "dayjs/plugin/localeData";

dayjs.extend(weekday);
dayjs.extend(localeData);

class GraphEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      graphName: props.match.params.graphName,
      isNewGraph: props.location?.state?.isNewGraph || false,
      graph: null,
      graphCount: "key",
      stores: [],
      filteredChats: [],
      tempStartTime: null,
      tempEndTime: null,
    };
  }

  UNSAFE_componentWillMount() {
    this.getGraph();
    this.getStores();
  }

  getStores() {
    StoreBackend.getStores(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({stores: res.data || []});
        }
      });
  }

  getGraph() {
    GraphBackend.getGraph(this.props.account.name, this.state.graphName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({graph: res.data}, () => this.loadFilteredChats());
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseGraphField(key, value) {
    if (["density"].includes(key)) {value = parseFloat(value);}
    return value;
  }

  updateGraphField(key, value) {
    value = this.parseGraphField(key, value);
    const graph = this.state.graph;
    graph[key] = value;
    this.setState({graph});
  }

  handleErrorChange(errorText) {
    this.updateGraphField("errorText", errorText);
  }

  generateGraphData() {
    this.updateGraphField("text", "");
    const graph = Setting.deepCopy(this.state.graph);
    graph.text = "";
    GraphBackend.updateGraph(this.state.graph.owner, this.state.graphName, graph)
      .then((res) => {
        if (res.status === "ok") {
          this.getGraph();
          this.loadFilteredChats();
          Setting.showMessage("success", i18next.t("general:Successfully generated"));
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to generate")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to generate")}: ${error}`);
      });
  }

  loadFilteredChats() {
    if (this.state.graph && this.state.graph.category === "Chats") {
      ChatBackend.getChats("admin", this.state.graph.store, "", "", "", "", "", "", "", this.state.graph.startTime, this.state.graph.endTime)
        .then((res) => {
          if (res.status === "ok") {
            this.setState({filteredChats: res.data || []});
          }
        });
    }
  }

  toDayjs = (rfc3339) => {
    if (!rfc3339) {return null;}
    const d = dayjs(rfc3339);
    return d.isValid() ? d : null;
  };

  toRFC3339 = (d) => (d ? d.format("YYYY-MM-DDTHH:mm:ssZ") : "");

  toDatetimeLocal = (rfc3339) => {
    if (!rfc3339) {return "";}
    const d = dayjs(rfc3339);
    return d.isValid() ? d.format("YYYY-MM-DDTHH:mm") : "";
  };

  fromDatetimeLocal = (str) => {
    if (!str) {return "";}
    const d = dayjs(str);
    return d.isValid() ? d.format("YYYY-MM-DDTHH:mm:ssZ") : "";
  };

  renderGraph() {
    const g = this.state.graph;
    const Field = ({label, children}) => (
      <div className="flex flex-col sm:flex-row gap-2 mt-5">
        <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{label}</label>
        <div className="flex-1">{children}</div>
      </div>
    );

    const layoutOptions = g.category === "Chats"
      ? [{value: "wordcloud", label: i18next.t("graph:Word Cloud")}]
      : [
        {value: "force", label: i18next.t("graph:Force")},
        {value: "circular", label: i18next.t("graph:Circular")},
        {value: "radial", label: i18next.t("graph:Radial")},
        {value: "grid", label: i18next.t("graph:Grid")},
        {value: "tree", label: i18next.t("graph:Tree")},
        {value: "none", label: i18next.t("general:None")},
      ];

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg">
        <div className="flex items-center justify-between px-6 py-4 border-b border-zinc-800">
          <h3 className="text-base font-medium text-white">{i18next.t("graph:Edit Graph")}</h3>
          <div className="flex gap-2">
            <button onClick={() => this.submitGraphEdit(false)} className="px-4 py-1.5 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Save")}</button>
            <button onClick={() => this.submitGraphEdit(true)} className="px-4 py-1.5 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Save & Exit")}</button>
            {this.state.isNewGraph && <button onClick={() => this.cancelGraphEdit()} className="px-4 py-1.5 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Cancel")}</button>}
          </div>
        </div>
        <div className="p-6">
          <Field label={Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))}>
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={g.name} onChange={e => this.updateGraphField("name", e.target.value)} />
          </Field>
          <Field label={Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))}>
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={g.displayName} onChange={e => this.updateGraphField("displayName", e.target.value)} />
          </Field>
          <Field label={Setting.getLabel(i18next.t("general:Category"), i18next.t("provider:Category - Tooltip"))}>
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={g.category || "Default"} onChange={e => this.updateGraphField("category", e.target.value)}>
              <option value="Default">{i18next.t("general:Default")}</option>
              <option value="Assets">{i18next.t("general:Assets")}</option>
              <option value="Chats">{i18next.t("general:Chats")}</option>
            </select>
          </Field>
          <Field label={Setting.getLabel(i18next.t("graph:Layout"), i18next.t("graph:Layout - Tooltip"))}>
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={g.layout || (g.category === "Chats" ? "wordcloud" : "force")} onChange={e => this.updateGraphField("layout", e.target.value)}>
              {layoutOptions.map(opt => <option key={opt.value} value={opt.value}>{opt.label}</option>)}
            </select>
          </Field>
          {g.category === "Chats" && (
            <>
              <Field label={Setting.getLabel(i18next.t("general:Store"), i18next.t("general:Store - Tooltip"))}>
                <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={g.store || ""} onChange={e => this.updateGraphField("store", e.target.value)}>
                  <option value="">--</option>
                  {this.state.stores.map(store => <option key={store.name} value={store.name}>{store.displayName || store.name}</option>)}
                </select>
              </Field>
              <Field label={Setting.getLabel(i18next.t("video:Start time (s)"), i18next.t("video:Start time (s) - Tooltip"))}>
                <input type="datetime-local" className="w-[300px] px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={this.toDatetimeLocal(g.startTime)} onChange={e => this.updateGraphField("startTime", this.fromDatetimeLocal(e.target.value))} />
              </Field>
              <Field label={Setting.getLabel(i18next.t("video:End time (s)"), i18next.t("video:End time (s) - Tooltip"))}>
                <input type="datetime-local" className="w-[300px] px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={this.toDatetimeLocal(g.endTime)} onChange={e => this.updateGraphField("endTime", this.fromDatetimeLocal(e.target.value))} />
              </Field>
              <Field label={Setting.getLabel(i18next.t("graph:Threshold"), i18next.t("graph:Threshold - Tooltip"))}>
                <input type="number" min={1} step={1} className="w-[200px] px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={g.density || 1} onChange={e => this.updateGraphField("density", e.target.value)} />
              </Field>
              <div className="mt-5 ml-40">
                <button onClick={() => this.generateGraphData()} className="px-4 py-1.5 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Generate")}</button>
              </div>
            </>
          )}
          {g.category !== "Chats" && (
            <Field label={Setting.getLabel(i18next.t("graph:Node density"), i18next.t("graph:Node density - Tooltip"))}>
              <input type="number" min={0.1} max={10} step={0.1} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={g.density || 5} onChange={e => this.updateGraphField("density", e.target.value)} />
            </Field>
          )}
          <Field label={Setting.getLabel(i18next.t("general:Text"), i18next.t("general:Text - Tooltip"))}>
            <div style={{height: "500px"}}>
              <Editor value={g.text} lang="json" fillHeight dark onChange={value => this.updateGraphField("text", value)} />
            </div>
          </Field>
          <Field label={i18next.t("general:Preview")}>
            <div key={this.state.graphCount} style={{height: "1000px", width: "100%"}}>
              {g.category === "Chats" ? (
                <GraphChatDataPage graphText={g.text} showBorder={true} onErrorChange={(errorText) => this.handleErrorChange(errorText)} />
              ) : (
                <GraphDataPage account={this.props.account} owner={g.owner} graphName={g.name} graphText={g.text} category={g.category} layout={g.layout} density={g.density} showBorder={true} onErrorChange={(errorText) => this.handleErrorChange(errorText)} />
              )}
            </div>
          </Field>
        </div>
      </div>
    );
  }

  renderFilteredChatsSection() {
    if (!this.state.graph || this.state.graph.category !== "Chats") {return null;}
    return <GraphChatTable chats={this.state.filteredChats} />;
  }

  submitGraphEdit(exitAfterSave) {
    const graph = Setting.deepCopy(this.state.graph);
    if (!exitAfterSave) {
      this.setState({graphCount: this.state.graphCount + "a"});
    }
    GraphBackend.updateGraph(this.state.graph.owner, this.state.graphName, graph)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({graphName: this.state.graph.name, isNewGraph: false});
            if (exitAfterSave) {this.props.history.push("/graphs");} else {this.props.history.push(`/graphs/${this.state.graph.name}`);}
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to save"));
            this.updateGraphField("name", this.state.graphName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`));
  }

  cancelGraphEdit() {
    if (this.state.isNewGraph) {
      GraphBackend.deleteGraph(this.state.graph)
        .then((res) => {
          if (res.status === "ok") {Setting.showMessage("success", i18next.t("general:Cancelled successfully")); this.props.history.push("/graphs");}
          else {Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);}
        })
        .catch(error => Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`));
    } else {
      this.props.history.push("/graphs");
    }
  }

  render() {
    return (
      <div>
        {this.state.graph !== null ? this.renderGraph() : null}
        {this.renderFilteredChatsSection()}
        <div className="mt-5 ml-10 flex gap-3">
          <button onClick={() => this.submitGraphEdit(false)} className="px-6 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Save")}</button>
          <button onClick={() => this.submitGraphEdit(true)} className="px-6 py-2 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewGraph && <button onClick={() => this.cancelGraphEdit()} className="px-6 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
}

export default GraphEditPage;
