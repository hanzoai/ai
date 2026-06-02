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
import {Cascader, Select} from "antd"; // eslint-disable-line unused-imports/no-unused-imports
import * as StoreBackend from "./backend/StoreBackend";
import * as StorageProviderBackend from "./backend/StorageProviderBackend";
import * as ProviderBackend from "./backend/ProviderBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import FileTree from "./FileTree";
import {ThemeDefault} from "./Conf";
import ExampleQuestionTable from "./table/ExampleQuestionTable";
import StoreAvatarUploader from "./AvatarUpload";
import {Link as LinkIcon} from "lucide-react";
import {NavItemTree} from "./component/nav-item-tree/NavItemTree";

const {Option} = Select;

class StoreEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      owner: props.match.params.owner,
      storeName: props.match.params.storeName,
      stores: [],
      iamStorageProviders: [],
      storageProviders: [],
      vectorStoreId: "",
      storageSubpath: "",
      modelProviders: [],
      embeddingProviders: [],
      textToSpeechProviders: [],
      speechToTextProviders: [],
      agentProviders: [],
      builtinTools: [],
      enableTtsStreaming: false,
      store: null,
      themeColor: ThemeDefault.colorPrimary,
      isNewStore: props.location?.state?.isNewStore || false,
    };
  }

  UNSAFE_componentWillMount() {
    this.getStore();
    this.getStores();
    this.getStorageProviders();
    this.getProviders();
  }

  renderProviderOption(provider, index) {
    return (
      <Option key={index} value={provider.name}>
        <img width={20} height={20} style={{marginBottom: "3px", marginRight: "10px"}}
          src={Setting.getProviderLogoURL({category: provider.category, type: provider.type})}
          alt={provider.name} />
        {provider.displayName} ({provider.name})
      </Option>
    );
  }

  getStore() {
    StoreBackend.getStore(this.state.owner, this.state.storeName)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data && typeof res.data2 === "string" && res.data2 !== "") {
            res.data.error = res.data2;
          }
          this.setState({store: res.data});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getStores() {
    StoreBackend.getStores(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({stores: res.data});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getStorageProviders() {
    StorageProviderBackend.getStorageProviders(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({iamStorageProviders: res.data});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getProviders() {
    ProviderBackend.getProviders(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            storageProviders: res.data.filter(provider => provider.category === "Storage"),
            modelProviders: res.data.filter(provider => provider.category === "Model"),
            embeddingProviders: res.data.filter(provider => provider.category === "Embedding"),
            textToSpeechProviders: res.data.filter(provider => provider.category === "Text-to-Speech"),
            speechToTextProviders: res.data.filter(provider => provider.category === "Speech-to-Text"),
            agentProviders: res.data.filter(provider => provider.category === "Agent"),
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseStoreField(key, value) {
    if (["score"].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateStoreField(key, value) {
    value = this.parseStoreField(key, value);
    const store = this.state.store;
    store[key] = value;
    this.setState({store: store});
  }

  isAIStorageProvider(storageProvider) {
    const providerSelected = this.state.storageProviders.concat(this.state.iamStorageProviders).find(v => v.name === storageProvider);
    return providerSelected && providerSelected.type === "OpenAI File System";
  }

  renderBuiltinTools() {
    const builtinToolsConfig = Setting.getBuiltinTools();
    const selectedTools = this.state.store.builtinTools || [];
    const options = builtinToolsConfig.map(category => ({
      value: category.category,
      label: `${category.icon} ${category.name}`,
      children: category.tools.map(tool => ({
        value: tool.name,
        label: (<div><div style={{fontWeight: 500, color: "#1890ff"}}>{tool.name}</div><div style={{fontSize: "12px", color: "#8c8c8c"}}>{tool.description}</div></div>),
      })),
    }));
    const value = selectedTools.map(tool => {
      const category = builtinToolsConfig.find(cat => cat.tools.some(t => t.name === tool));
      return category ? [category.category, tool] : null;
    }).filter(v => v);

    return (
      <Cascader multiple maxTagCount="responsive" style={{width: "100%"}} placeholder={i18next.t("store:Select builtin tools")} options={options} value={value} onChange={(values) => {this.updateStoreField("builtinTools", values.map(v => v[1]));}} showCheckedStrategy="SHOW_CHILD" popupClassName="builtin-tools-cascader" />
    );
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

  renderStore() {
    return (
      <div className="rounded-lg border border-border bg-card p-6 ml-1">
        <div className="flex items-center gap-4 mb-6">
          <h2 className="text-lg font-semibold text-foreground">{i18next.t("store:Edit Store")}</h2>
          <button onClick={() => this.submitStoreEdit(false, undefined)} className="rounded-md border border-border px-3 py-1.5 text-sm text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Save")}</button>
          <button onClick={() => this.submitStoreEdit(true, undefined)} className="rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewStore && <button onClick={() => this.cancelStoreEdit()} className="rounded-md border border-border px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Cancel")}</button>}
        </div>

        {this.renderFormRow(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50" value={this.state.store.name} disabled={Setting.isUserBoundToStore(this.props.account)} onChange={e => this.updateStoreField("name", e.target.value)} />)}
        {this.renderFormRow(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.displayName} onChange={e => this.updateStoreField("displayName", e.target.value)} />)}
        {this.renderFormRow(i18next.t("general:Title"), i18next.t("general:Title - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.title} onChange={e => this.updateStoreField("title", e.target.value)} />)}
        {this.renderFormRow(i18next.t("general:Avatar"), i18next.t("general:Avatar - Tooltip"), <StoreAvatarUploader store={this.state.store} onUpdate={(newUrl) => this.updateStoreField("avatar", newUrl)} onUploadComplete={() => this.submitStoreEdit(false, undefined)} />)}
        {this.renderFormRow(i18next.t("store:Is default"), i18next.t("store:Is default - Tooltip"), <input type="checkbox" checked={this.state.store.isDefault} onChange={e => this.updateStoreField("isDefault", e.target.checked)} className="w-4 h-4 rounded border-border accent-primary" />)}
        {this.renderFormRow(i18next.t("general:State"), i18next.t("general:State - Tooltip"), <select className="w-48 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.state} onChange={e => this.updateStoreField("state", e.target.value)}><option value="Active">{i18next.t("general:Active")}</option><option value="Inactive">{i18next.t("general:Inactive")}</option></select>)}
        {this.renderFormRow(i18next.t("store:Storage provider"), i18next.t("store:Storage provider - Tooltip"), <Select virtual={false} style={{width: "100%"}} value={this.state.store.storageProvider} onChange={value => this.updateStoreField("storageProvider", value)}>{this.state.storageProviders.concat(this.state.iamStorageProviders).map((provider, index) => this.renderProviderOption(provider, index))}</Select>)}
        {this.renderFormRow(i18next.t("store:Storage subpath"), i18next.t("store:Storage subpath - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.storageSubpath} onChange={e => this.updateStoreField("storageSubpath", e.target.value)} />)}
        {this.isAIStorageProvider(this.state.store.storageProvider) && this.renderFormRow(i18next.t("store:Vector store id"), i18next.t("store:Vector store id - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.vectorStoreId} onChange={e => this.updateStoreField("vectorStoreId", e.target.value)} />)}
        {this.renderFormRow(i18next.t("store:Image provider"), i18next.t("store:Image provider - Tooltip"), <Select virtual={false} style={{width: "100%"}} value={this.state.store.imageProvider} onChange={value => this.updateStoreField("imageProvider", value)}><Option key="none" value="">{i18next.t("general:empty")}</Option>{this.state.iamStorageProviders.map((provider, index) => this.renderProviderOption(provider, index))}</Select>)}
        {this.renderFormRow(i18next.t("store:Split provider"), i18next.t("store:Split provider - Tooltip"), <select className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.splitProvider} onChange={e => this.updateStoreField("splitProvider", e.target.value)}>{["Default", "Basic", "QA", "Markdown"].map(name => <option key={name} value={name}>{name}</option>)}</select>)}
        {this.renderFormRow(i18next.t("store:Search provider"), i18next.t("store:Search provider - Tooltip"), <select className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.searchProvider} onChange={e => this.updateStoreField("searchProvider", e.target.value)}>{["Default", "Hierarchy"].map(name => <option key={name} value={name}>{name}</option>)}</select>)}
        {this.renderFormRow(i18next.t("provider:Model provider"), i18next.t("provider:Model provider - Tooltip"), <Select virtual={false} style={{width: "100%"}} value={this.state.store.modelProvider} onChange={value => this.updateStoreField("modelProvider", value)}>{this.state.modelProviders.map((provider, index) => this.renderProviderOption(provider, index))}</Select>)}
        {this.renderFormRow(i18next.t("store:Embedding provider"), i18next.t("store:Embedding provider - Tooltip"), <Select virtual={false} style={{width: "100%"}} value={this.state.store.embeddingProvider} onChange={value => this.updateStoreField("embeddingProvider", value)}><Option key="none" value="">{i18next.t("general:empty")}</Option>{this.state.embeddingProviders.map((provider, index) => this.renderProviderOption(provider, index))}</Select>)}
        {this.renderFormRow(i18next.t("store:Agent provider"), i18next.t("store:Agent provider - Tooltip"), <Select virtual={false} style={{width: "100%"}} value={this.state.store.agentProvider} onChange={value => this.updateStoreField("agentProvider", value)}><Option key="Empty" value="">{i18next.t("general:empty")}</Option>{this.state.agentProviders.map((provider, index) => this.renderProviderOption(provider, index))}</Select>)}
        {this.renderFormRow(i18next.t("store:Builtin tools"), i18next.t("store:Builtin tools - Tooltip"), this.renderBuiltinTools())}
        {this.renderFormRow(i18next.t("store:Text-to-Speech provider"), i18next.t("store:Text-to-Speech provider - Tooltip"), <Select virtual={false} style={{width: "100%"}} value={this.state.store.textToSpeechProvider} onChange={value => this.updateStoreField("textToSpeechProvider", value)}><Option key="Empty" value="">{i18next.t("general:empty")}</Option><Option key="Browser Built-In" value="Browser Built-In">Browser Built-In</Option>{this.state.textToSpeechProviders.map((provider, index) => this.renderProviderOption(provider, index))}</Select>)}
        {this.renderFormRow(i18next.t("store:Enable TTS streaming"), i18next.t("store:Enable TTS streaming - Tooltip"), <input type="checkbox" checked={this.state.store.enableTtsStreaming} onChange={e => this.updateStoreField("enableTtsStreaming", e.target.checked)} className="w-4 h-4 rounded border-border accent-primary" />)}
        {this.renderFormRow(i18next.t("store:Speech-to-Text provider"), i18next.t("store:Speech-to-Text provider - Tooltip"), <Select virtual={false} style={{width: "100%"}} value={this.state.store.speechToTextProvider} onChange={value => this.updateStoreField("speechToTextProvider", value)}><Option key="Empty" value="">{i18next.t("general:empty")}</Option><Option key="Browser Built-In" value="Browser Built-In">Browser Built-In</Option>{this.state.speechToTextProviders.map((provider, index) => this.renderProviderOption(provider, index))}</Select>)}
        {this.renderFormRow(i18next.t("store:Frequency"), i18next.t("store:Frequency - Tooltip"), <input type="number" min={0} className="w-32 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.frequency} onChange={e => this.updateStoreField("frequency", parseInt(e.target.value) || 0)} />)}
        {this.renderFormRow(i18next.t("store:Memory limit"), i18next.t("store:Memory limit - Tooltip"), <input type="number" min={0} className="w-32 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.memoryLimit} onChange={e => this.updateStoreField("memoryLimit", parseInt(e.target.value) || 0)} />)}
        {this.renderFormRow(i18next.t("store:Limit minutes"), i18next.t("store:Limit minutes - Tooltip"), <input type="number" min={0} className="w-32 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.limitMinutes} onChange={e => this.updateStoreField("limitMinutes", parseInt(e.target.value) || 0)} />)}
        {this.renderFormRow(i18next.t("store:Welcome"), i18next.t("store:Welcome - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.welcome} onChange={e => this.updateStoreField("welcome", e.target.value)} />)}
        {this.renderFormRow(i18next.t("store:Welcome title"), i18next.t("store:Welcome title - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.welcomeTitle} onChange={e => this.updateStoreField("welcomeTitle", e.target.value)} />)}
        {this.renderFormRow(i18next.t("store:Welcome text"), i18next.t("store:Welcome text - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.welcomeText} onChange={e => this.updateStoreField("welcomeText", e.target.value)} />)}
        {this.renderFormRow(i18next.t("store:Prompt"), i18next.t("store:Prompt - Tooltip"), <textarea className="w-full min-h-[2.5rem] max-h-[22rem] resize-y rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.prompt} onChange={e => this.updateStoreField("prompt", e.target.value)} rows={3} />)}
        {this.renderFormRow(i18next.t("store:Example questions"), i18next.t("store:Example questions - Tooltip"), <ExampleQuestionTable table={this.state.store.exampleQuestions} onUpdateTable={(exampleQuestions) => this.updateStoreField("exampleQuestions", exampleQuestions)} />)}
        {this.renderFormRow(i18next.t("store:Knowledge count"), i18next.t("store:Knowledge count - Tooltip"), <input type="number" min={0} max={100} className="w-32 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.knowledgeCount} onChange={e => this.updateStoreField("knowledgeCount", parseInt(e.target.value) || 0)} />)}
        {this.renderFormRow(i18next.t("store:Suggestion count"), i18next.t("store:Suggestion count - Tooltip"), <input type="number" min={0} max={10} className="w-32 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.suggestionCount} onChange={e => this.updateStoreField("suggestionCount", parseInt(e.target.value) || 0)} />)}
        {this.renderFormRow(i18next.t("general:Site setting"), i18next.t("general:Site setting - Tooltip"), <div className="space-y-5">
          {this.renderFormRow(i18next.t("store:Theme color"), i18next.t("store:Theme color - Tooltip"), <input type="color" value={this.state.store.themeColor} onChange={e => this.updateStoreField("themeColor", e.target.value)} className="w-10 h-10 rounded cursor-pointer" />)}
          {this.renderFormRow(i18next.t("store:Navbar items"), i18next.t("store:Navbar items - Tooltip"), <NavItemTree disabled={!Setting.isAdminUser(this.props.account)} checkedKeys={this.state.store.navItems ?? ["all"]} defaultExpandedKeys={["all"]} onCheck={(checked) => this.updateStoreField("navItems", checked)} />)}
          {this.renderFormRow(i18next.t("general:HTML title"), i18next.t("general:HTML title - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.htmlTitle} onChange={e => this.updateStoreField("htmlTitle", e.target.value)} />)}
          {this.renderFormRow(i18next.t("general:Favicon URL"), i18next.t("general:Favicon URL - Tooltip"), <div className="relative"><LinkIcon className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" /><input className="w-full h-9 rounded-md border border-border bg-background pl-9 pr-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.faviconUrl} onChange={e => this.updateStoreField("faviconUrl", e.target.value)} /></div>)}
          {this.renderFormRow(i18next.t("general:Logo URL"), i18next.t("general:Logo URL - Tooltip"), <div className="relative"><LinkIcon className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" /><input className="w-full h-9 rounded-md border border-border bg-background pl-9 pr-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.logoUrl} onChange={e => this.updateStoreField("logoUrl", e.target.value)} /></div>)}
          {this.renderFormRow(i18next.t("general:Footer HTML"), i18next.t("general:Footer HTML - Tooltip"), <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.store.footerHtml} onChange={e => this.updateStoreField("footerHtml", e.target.value)} />)}
        </div>)}
        {this.renderFormRow(i18next.t("store:Vector stores"), i18next.t("store:Vector stores - Tooltip"), <Select virtual={false} mode="tags" style={{width: "100%"}} value={this.state.store.vectorStores} onChange={value => this.updateStoreField("vectorStores", value)}>{this.state.stores?.filter(item => item.name !== this.state.store.name).map((item) => <Option key={item.name} value={item.name}>{`${item.displayName} (${item.name})`}</Option>)}</Select>)}
        {this.renderFormRow(i18next.t("store:Child stores"), i18next.t("store:Child stores - Tooltip"), <Select virtual={false} mode="tags" style={{width: "100%"}} value={this.state.store.childStores} onChange={value => this.updateStoreField("childStores", value)}>{this.state.stores?.filter(item => item.name !== this.state.store.name).map((item) => <Option key={item.name} value={item.name}>{`${item.displayName} (${item.name})`}</Option>)}</Select>)}
        {this.renderFormRow(i18next.t("store:Child model providers"), i18next.t("store:Child model providers - Tooltip"), <Select virtual={false} mode="tags" style={{width: "100%"}} value={this.state.store.childModelProviders} onChange={value => this.updateStoreField("childModelProviders", value)}>{this.state.modelProviders?.map((item, index) => this.renderProviderOption(item, index))}</Select>)}
        {this.renderFormRow(i18next.t("store:Forbidden words"), i18next.t("store:Forbidden words - Tooltip"), <Select virtual={false} mode="tags" style={{width: "100%"}} value={this.state.store.forbiddenWords} onChange={value => this.updateStoreField("forbiddenWords", value)} />)}
        {this.renderFormRow(i18next.t("store:Show auto read"), i18next.t("store:Show auto read - Tooltip"), <input type="checkbox" checked={this.state.store.showAutoRead} onChange={e => this.updateStoreField("showAutoRead", e.target.checked)} className="w-4 h-4 rounded border-border accent-primary" />)}
        {this.renderFormRow(i18next.t("store:Disable file upload"), i18next.t("store:Disable file upload - Tooltip"), <input type="checkbox" checked={this.state.store.disableFileUpload} onChange={e => this.updateStoreField("disableFileUpload", e.target.checked)} className="w-4 h-4 rounded border-border accent-primary" />)}
        {this.renderFormRow(i18next.t("store:Hide thinking"), i18next.t("store:Hide thinking - Tooltip"), <input type="checkbox" checked={this.state.store.hideThinking} onChange={e => this.updateStoreField("hideThinking", e.target.checked)} className="w-4 h-4 rounded border-border accent-primary" />)}
        {this.renderFormRow(i18next.t("store:File tree"), i18next.t("store:File tree - Tooltip"), <FileTree account={this.props.account} store={this.state.store} onUpdateStore={(store) => {this.setState({store: store});this.submitStoreEdit(undefined, store);}} onRefresh={() => this.getStore()} />)}
      </div>
    );
  }

  submitStoreEdit(exitAfterSave, storeParam) {
    let store = Setting.deepCopy(this.state.store);
    if (storeParam) {store = storeParam;}
    store.fileTree = undefined;
    StoreBackend.updateStore(this.state.store.owner, this.state.storeName, store)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.setThemeColor(this.state.store.themeColor);
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({storeName: this.state.store.name, isNewStore: false});
            window.dispatchEvent(new Event("storesChanged"));
            if (exitAfterSave) {this.props.history.push("/stores");} else {this.props.history.push(`/stores/${this.state.store.owner}/${this.state.store.name}`);}
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
            this.updateStoreField("name", this.state.storeName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);});
  }

  cancelStoreEdit() {
    if (this.state.isNewStore) {
      StoreBackend.deleteStore(this.state.store)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            window.dispatchEvent(new Event("storesChanged"));
            this.props.history.push("/stores");
          } else {Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);}
        })
        .catch(error => {Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);});
    } else {this.props.history.push("/stores");}
  }

  render() {
    return (
      <div>
        {this.state.store !== null ? this.renderStore() : null}
        <div className="mt-5 ml-10 flex gap-4">
          <button onClick={() => this.submitStoreEdit(false, undefined)} className="rounded-md border border-border px-5 py-2 text-sm text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Save")}</button>
          <button onClick={() => this.submitStoreEdit(true, undefined)} className="rounded-md bg-primary px-5 py-2 text-sm text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewStore && <button onClick={() => this.cancelStoreEdit()} className="rounded-md border border-border px-5 py-2 text-sm text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
}

export default StoreEditPage;
