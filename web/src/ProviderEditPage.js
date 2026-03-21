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
import {Link2, Loader2} from "lucide-react";
import * as ProviderBackend from "./backend/ProviderBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import copy from "copy-to-clipboard";
import FileSaver from "file-saver";
import McpToolsTable from "./table/McpToolsTable";
import ModelTestWidget from "./common/TestModelWidget";
import TtsTestWidget from "./common/TestTtsWidget";
import EmbedTestWidget from "./common/TestEmbedWidget";
import TestScanWidget from "./common/TestScanWidget";
import Editor from "./common/Editor";

class ProviderEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      providerName: props.match.params.providerName,
      provider: null,
      originalProvider: null,
      refreshButtonLoading: false,
      isNewProvider: props.location?.state?.isNewProvider || false,
    };
  }

  UNSAFE_componentWillMount() {
    this.getProvider();
  }

  getProvider() {
    ProviderBackend.getProvider("admin", this.state.providerName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({provider: res.data, originalProvider: Setting.deepCopy(res.data)});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getClientIdLabel(provider) {
    if (["Model", "Embedding"].includes(provider.category)) {
      if (provider.type === "Tencent Cloud") {return Setting.getLabel(i18next.t("general:Secret ID"), i18next.t("general:Secret ID - Tooltip"));}
      if (provider.type === "Baidu Cloud") {return Setting.getLabel(i18next.t("provider:API key"), i18next.t("provider:API key - Tooltip"));}
      if (provider.type === "Azure") {return Setting.getLabel(i18next.t("provider:Deployment name"), i18next.t("provider:Deployment name - Tooltip"));}
      if (provider.type === "MiniMax") {return Setting.getLabel(i18next.t("provider:Group ID"), i18next.t("provider:Group ID - Tooltip"));}
    }
    if (provider.category === "Storage") {return Setting.getLabel(i18next.t("store:Storage subpath"), i18next.t("store:Storage subpath - Tooltip"));}
    if (provider.category === "Bot" && provider.type === "Tencent") {return Setting.getLabel(i18next.t("provider:Bot ID"), i18next.t("provider:Bot ID - Tooltip"));}
    return Setting.getLabel(i18next.t("provider:Client ID"), i18next.t("provider:Client ID - Tooltip"));
  }

  getNetworkLabel(provider) {
    if (provider.category === "Blockchain" && provider.type === "ChainMaker") {return Setting.getLabel(i18next.t("general:Node address"), i18next.t("general:Node address - Tooltip"));}
    return Setting.getLabel(i18next.t("general:Network"), i18next.t("general:Network - Tooltip"));
  }

  getProviderUrlLabel(provider) {
    if (["Model", "Blockchain"].includes(provider.category)) {
      if (provider.type === "Volcano Engine") {return Setting.getLabel(i18next.t("provider:Endpoint ID"), i18next.t("provider:Endpoint ID - Tooltip"));}
    }
    return Setting.getLabel(i18next.t("general:Provider URL"), i18next.t("general:Provider URL - Tooltip"));
  }

  getRegionLabel(provider) {
    if (provider.category === "Blockchain" && provider.type === "ChainMaker") {return Setting.getLabel(i18next.t("general:Org ID"), i18next.t("general:Org ID - Tooltip"));}
    if (provider.category === "Bot" && provider.type === "Tencent") {return Setting.getLabel(i18next.t("provider:AES key"), i18next.t("provider:AES key - Tooltip"));}
    return Setting.getLabel(i18next.t("general:Region"), i18next.t("general:Region - Tooltip"));
  }

  getClientSecretLabel(provider) {
    if (["Storage", "Embedding", "Text-to-Speech", "Speech-to-Text"].includes(provider.category)) {
      if (provider.type === "Baidu Cloud") {return Setting.getLabel(i18next.t("general:Access secret"), i18next.t("general:Access secret - Tooltip"));}
      return Setting.getLabel(i18next.t("general:Secret key"), i18next.t("general:Secret key - Tooltip"));
    }
    if (provider.category === "Model") {return Setting.getLabel(i18next.t("provider:API key"), i18next.t("provider:API key - Tooltip"));}
    if (provider.category === "Blockchain" && provider.type === "Ethereum") {return Setting.getLabel(i18next.t("provider:Private key"), i18next.t("provider:Private key - Tooltip"));}
    if (provider.category === "Bot" && provider.type === "Tencent") {return Setting.getLabel(i18next.t("provider:Token"), i18next.t("provider:Token - Tooltip"));}
    return Setting.getLabel(i18next.t("provider:Client secret"), i18next.t("provider:Client secret - Tooltip"));
  }

  getContractNameLabel(provider) {
    if (provider.category === "Blockchain" && provider.type === "Ethereum") {return Setting.getLabel(i18next.t("provider:Contract address"), i18next.t("provider:Contract address - Tooltip"));}
    return Setting.getLabel(i18next.t("provider:Contract name"), i18next.t("provider:Contract name - Tooltip"));
  }

  parseProviderField(key, value) {
    if (["topK"].includes(key)) {value = Setting.myParseInt(value);}
    else if (["temperature", "topP", "frequencyPenalty", "presencePenalty"].includes(key)) {value = Setting.myParseFloat(value);}
    return value;
  }

  parseMcpToolsField(key, value) {
    if ([""].includes(key)) {value = Setting.myParseInt(value);}
    return value;
  }

  updateMcpToolsField(key, value) {
    value = this.parseMcpToolsField(key, value);
    const provider = this.state.provider;
    provider[key] = value;
    this.setState({provider});
  }

  updateProviderField(key, value) {
    value = this.parseProviderField(key, value);
    const provider = this.state.provider;
    provider[key] = value;
    this.setState({provider});
  }

  isTemperatureEnabled(provider) {
    if (provider.category === "Model") {
      if (["OpenRouter", "iFlytek", "Hugging Face", "Baidu Cloud", "MiniMax", "Gemini", "Alibaba Cloud", "Baichuan", "Volcano Engine", "DeepSeek", "StepFun", "Tencent Cloud", "Mistral", "Yi", "Silicon Flow", "Ollama", "Writer"].includes(provider.type)) {return true;}
      if (provider.type === "OpenAI") {
        if (provider.subType.includes("o1") || provider.subType.includes("o3") || provider.subType.includes("o4")) {return false;}
        return true;
      }
    }
    return false;
  }

  isTopPEnabled(provider) {
    if (provider.category === "Model") {
      if (["OpenRouter", "Baidu Cloud", "Gemini", "Alibaba Cloud", "Baichuan", "Volcano Engine", "DeepSeek", "StepFun", "Tencent Cloud", "Mistral", "Yi", "Silicon Flow", "Ollama", "Writer"].includes(provider.type)) {return true;}
      if (provider.type === "OpenAI") {
        if (provider.subType.includes("o1") || provider.subType.includes("o3") || provider.subType.includes("o4")) {return false;}
        return true;
      }
    }
    return false;
  }

  InputSlider({min, max, step, value, onChange, disabled}) {
    return (
      <div className="flex items-center gap-3 flex-1">
        <input type="number" min={min} max={max} step={step} value={value} onChange={e => onChange(parseFloat(e.target.value))} disabled={disabled} className="w-20 px-2 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" />
        <input type="range" min={min} max={max} step={step} value={value} onChange={e => onChange(parseFloat(e.target.value))} disabled={disabled} className="flex-1 accent-white" />
      </div>
    );
  }

  renderProvider() {
    const isRemote = this.state.provider.isRemote;
    const p = this.state.provider;

    const Field = ({label, children}) => (
      <div className="flex flex-col sm:flex-row gap-2 mt-5">
        <label className="sm:w-40 shrink-0 text-sm text-zinc-400 pt-2">{label}</label>
        <div className="flex-1">{children}</div>
      </div>
    );

    const TextInput = ({value, onChange, disabled, type, placeholder, prefix}) => (
      <div className="flex items-center">
        {prefix && <span className="text-zinc-500 mr-2">{prefix}</span>}
        <input type={type || "text"} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={value || ""} onChange={onChange} disabled={disabled} placeholder={placeholder} />
      </div>
    );

    const categoryOptions = ["Storage", "Model", "Embedding", "Agent", "Public Cloud", "Private Cloud", "Blockchain", "Video", "Text-to-Speech", "Speech-to-Text", "Bot", "Scan"];
    const currencyOptions = ["USD", "CNY", "EUR", "JPY", "GBP", "AUD", "CAD", "CHF", "HKD", "SGD"];

    const handleCategoryChange = (value) => {
      this.updateProviderField("category", value);
      const defaults = {
        "Storage": {type: "Local File System"}, "Model": {type: "OpenAI", subType: "gpt-4"}, "Embedding": {type: "OpenAI", subType: "AdaSimilarity"},
        "Agent": {type: "MCP", subType: "Default"}, "Video": {type: "AWS"}, "Text-to-Speech": {type: "Alibaba Cloud", subType: "cosyvoice-v1"},
        "Speech-to-Text": {type: "Alibaba Cloud", subType: "paraformer-realtime-v1"}, "Private Cloud": {type: "Kubernetes"}, "Bot": {type: "Tencent", subType: "WeCom Bot"},
        "Scan": {type: "Nmap", subType: "Default"},
      };
      if (defaults[value]) {
        Object.entries(defaults[value]).forEach(([k, v]) => this.updateProviderField(k, v));
      }
    };

    const handleTypeChange = (value) => {
      this.updateProviderField("type", value);
      // Type-specific subType defaults (keeping all the original logic)
      const typeSubTypeMap = {
        Model: {OpenAI: "gpt-4", Gemini: "gemini-pro", OpenRouter: "openai/gpt-4", iFlytek: "spark-v2.0", "Baidu Cloud": "ernie-4.0-8k", MiniMax: "abab5-chat", Claude: "claude-opus-4-0", "Hugging Face": "gpt2", ChatGLM: "chatglm2-6b", Ollama: "llama3.3:70b", Local: "custom-model", Azure: "gpt-4", Cohere: "command", Dummy: "Dummy", "Alibaba Cloud": "qwen-long", Moonshot: "Moonshot-v1-8k", "Amazon Bedrock": "Claude", Baichuan: "Baichuan2-Turbo", "Volcano Engine": "Doubao-lite-4k", DeepSeek: "deepseek-chat", StepFun: "step-1-8k", "Tencent Cloud": "hunyuan-turbo", Yi: "yi-lightning", "Silicon Flow": "deepseek-ai/DeepSeek-R1", GitHub: "gpt-4o", Writer: "palmyra-x5"},
        Embedding: {OpenAI: "AdaSimilarity", Gemini: "embedding-001", "Hugging Face": "sentence-transformers/all-MiniLM-L6-v2", Cohere: "embed-english-v2.0", "Baidu Cloud": "Embedding-V1", Local: "custom-embedding", Azure: "AdaSimilarity", Dummy: "Dummy"},
        Agent: {MCP: "Default", A2A: "Default"},
        "Text-to-Speech": {"Alibaba Cloud": "cosyvoice-v1"},
        "Speech-to-Text": {"Alibaba Cloud": "paraformer-realtime-v1"},
        Bot: {Tencent: "WeCom Bot"},
      };
      const map = typeSubTypeMap[p.category];
      if (map && map[value]) {this.updateProviderField("subType", map[value]);}
    };

    const showClientId = !(p.category === "Private Cloud" && p.type === "Kubernetes") && p.category !== "Scan" &&
      (((p.category === "Embedding" && ["Baidu Cloud", "Tencent Cloud"].includes(p.type)) || (p.category === "Storage" && p.type !== "OpenAI File System")) ||
      (p.category === "Model" && p.type === "MiniMax") || (p.category === "Blockchain" && !["ChainMaker", "Ethereum"].includes(p.type)) ||
      ((p.category === "Model" || p.category === "Embedding") && p.type === "Azure") ||
      !["Storage", "Model", "Embedding", "Text-to-Speech", "Speech-to-Text", "Agent", "Blockchain"].includes(p.category));

    const showClientSecret = !((p.category === "Storage" && p.type !== "OpenAI File System") || (p.category === "Agent" && p.type === "MCP") ||
      (p.category === "Blockchain" && p.type === "ChainMaker") || p.category === "Scan" || p.type === "Dummy" || p.type === "Ollama");

    const showSubType = ["Model", "Embedding", "Agent", "Text-to-Speech", "Speech-to-Text", "Bot"].includes(p.category);
    const showRegion = !["Storage", "Model", "Embedding", "Agent", "Text-to-Speech", "Speech-to-Text", "Scan"].includes(p.category) && !(p.category === "Blockchain" && p.type === "Ethereum") && !(p.category === "Private Cloud" && p.type === "Kubernetes");

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg">
        <div className="flex items-center justify-between px-6 py-4 border-b border-zinc-800">
          <h3 className="text-base font-medium text-white">{isRemote ? i18next.t("general:View") : i18next.t("provider:Edit Provider")}</h3>
          {!isRemote && (
            <div className="flex gap-2">
              <button onClick={() => this.submitProviderEdit(false)} className="px-4 py-1.5 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Save")}</button>
              <button onClick={() => this.submitProviderEdit(true)} className="px-4 py-1.5 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Save & Exit")}</button>
              {this.state.isNewProvider && <button onClick={() => this.cancelProviderEdit()} className="px-4 py-1.5 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Cancel")}</button>}
            </div>
          )}
        </div>
        <div className="p-6">
          <Field label={Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))}>
            <span className="text-zinc-300 text-sm"> this.updateProviderField("name", e.target.value)} />
          </Field>
          <Field label={Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))}>
            <span className="text-zinc-300 text-sm"> this.updateProviderField("displayName", e.target.value)} />
          </Field>
          <Field label={Setting.getLabel(i18next.t("general:Category"), i18next.t("provider:Category - Tooltip"))}>
            <select disabled={isRemote} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.category} onChange={e => handleCategoryChange(e.target.value)}>
              {categoryOptions.map(c => <option key={c} value={c}>{c}</option>)}
            </select>
          </Field>
          <Field label={Setting.getLabel(i18next.t("general:Type"), i18next.t("general:Type - Tooltip"))}>
            <select disabled={isRemote} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.type} onChange={e => handleTypeChange(e.target.value)}>
              {Setting.getProviderTypeOptions(p.category).map((item) => (
                <option key={item.name} value={item.name}>{item.name}</option>
              ))}
            </select>
          </Field>
          {showSubType && (
            <Field label={Setting.getLabel(i18next.t("provider:Sub type"), i18next.t("provider:Sub type - Tooltip"))}>
              {p.type === "Ollama" ? (
                <span className="text-zinc-300 text-sm"> this.updateProviderField("subType", e.target.value)} placeholder="Please select or enter the model name" />
              ) : (
                <select disabled={isRemote} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.subType} onChange={e => this.updateProviderField("subType", e.target.value)}>
                  {Setting.getProviderSubTypeOptions(p.category, p.type).map((item) => (
                    <option key={item.id} value={item.id}>{item.name}</option>
                  ))}
                </select>
              )}
            </Field>
          )}
          {p.type === "Cohere" && p.category === "Embedding" && (
            <Field label={Setting.getLabel(i18next.t("provider:Input type"), i18next.t("provider:Input type - Tooltip"))}>
              <select disabled={isRemote} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.clientId} onChange={e => this.updateProviderField("clientId", e.target.value)}>
                {["search_document", "search_query"].map(v => <option key={v} value={v}>{v}</option>)}
              </select>
            </Field>
          )}
          {showClientId && (
            <Field label={this.getClientIdLabel(p)}>
              <span className="text-zinc-300 text-sm"> this.updateProviderField("clientId", e.target.value)} />
            </Field>
          )}
          {p.type === "Local" && (
            <Field label={Setting.getLabel(i18next.t("provider:Compatible provider"), i18next.t("provider:Compatible provider - Tooltip"))}>
              <span className="text-zinc-300 text-sm"> this.updateProviderField("compatibleProvider", e.target.value)} placeholder="Please select or enter the compatible provider" />
            </Field>
          )}
          {(p.category === "Model" && (p.type === "Local" || p.type === "Ollama")) && (
            <>
              <Field label={Setting.getLabel(i18next.t("provider:Input price / 1k tokens"), i18next.t("provider:Input price / 1k tokens - Tooltip"))}>
                <input type="number" min={0} className="w-40 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={p.inputPricePerThousandTokens} onChange={e => this.updateProviderField("inputPricePerThousandTokens", parseFloat(e.target.value))} />
              </Field>
              <Field label={Setting.getLabel(i18next.t("provider:Output price / 1k tokens"), i18next.t("provider:Output price / 1k tokens - Tooltip"))}>
                <input type="number" min={0} className="w-40 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={p.outputPricePerThousandTokens} onChange={e => this.updateProviderField("outputPricePerThousandTokens", parseFloat(e.target.value))} />
              </Field>
            </>
          )}
          {(p.category === "Embedding" && (p.type === "Local" || p.type === "Ollama")) && (
            <Field label={Setting.getLabel(i18next.t("provider:Input price / 1k tokens"), i18next.t("provider:Input price / 1k tokens - Tooltip"))}>
              <input type="number" min={0} className="w-40 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={p.inputPricePerThousandTokens} onChange={e => this.updateProviderField("inputPricePerThousandTokens", parseFloat(e.target.value))} />
            </Field>
          )}
          {(p.type === "Local" || p.type === "Ollama") && (
            <Field label={Setting.getLabel(i18next.t("provider:Currency"), i18next.t("provider:Currency - Tooltip"))}>
              <select disabled={isRemote} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.currency} onChange={e => this.updateProviderField("currency", e.target.value)}>
                {currencyOptions.map(c => <option key={c} value={c}>{c}</option>)}
              </select>
            </Field>
          )}
          {p.category === "Text-to-Speech" && p.type === "Alibaba Cloud" && p.subType === "cosyvoice-v1" && (
            <Field label={Setting.getLabel(i18next.t("provider:Flavor"), i18next.t("provider:Flavor - Tooltip"))}>
              <select disabled={isRemote} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.flavor} onChange={e => this.updateProviderField("flavor", e.target.value)}>
                {Setting.getTtsFlavorOptions(p.type, p.subType).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
              </select>
            </Field>
          )}
          {showClientSecret && (
            <Field label={this.getClientSecretLabel(p)}>
              <input type="password" disabled={isRemote} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.clientSecret || ""} onChange={e => this.updateProviderField("clientSecret", e.target.value)} />
            </Field>
          )}
          {p.category === "Model" && p.type === "Claude" && Setting.getThinkingModelMaxTokens(p.subType) !== 0 && (
            <>
              <Field label={Setting.getLabel(i18next.t("provider:Enable thinking"), i18next.t("provider:Enable thinking - Tooltip"))}>
                <label className="relative inline-flex items-center cursor-pointer">
                  <input type="checkbox" disabled={isRemote} checked={p.enableThinking} onChange={e => this.updateProviderField("enableThinking", e.target.checked)} className="sr-only peer" />
                  <div className="w-9 h-5 bg-zinc-700 rounded-full peer peer-checked:bg-white peer-checked:after:translate-x-full after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-zinc-900 after:rounded-full after:h-4 after:w-4 after:transition-all" />
                </label>
              </Field>
              {p.enableThinking && (
                <Field label={Setting.getLabel(i18next.t("provider:Thinking tokens"), i18next.t("provider:Thinking tokens - Tooltip"))}>
                  <input type="number" min={1024} max={Setting.getThinkingModelMaxTokens(p.subType) - 1} className="w-40 px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={p.topK || 1024} onChange={e => this.updateProviderField("topK", parseInt(e.target.value))} />
                </Field>
              )}
            </>
          )}
          {p.category === "Agent" && (
            <>
              <Field label={Setting.getLabel(i18next.t("provider:MCP servers"), i18next.t("provider:MCP servers - Tooltip"))}>
                <div style={{height: "500px"}}>
                  <Editor editable={!isRemote} value={p.text} lang="json" fillHeight dark onChange={value => this.updateProviderField("text", value)} />
                </div>
                <button disabled={isRemote} onClick={() => this.refreshMcpTools()} className="mt-3 px-4 py-1.5 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors disabled:opacity-50 flex items-center gap-1">
                  {this.state.refreshButtonLoading && <Loader2 className="w-3 h-3 animate-spin" />}
                  {i18next.t("provider:Refresh MCP tools")}
                </button>
              </Field>
              <Field label={Setting.getLabel(i18next.t("provider:MCP tools"), i18next.t("provider:MCP tools - Tooltip"))}>
                <McpToolsTable title={i18next.t("provider:MCP tools")} table={p.mcpTools} onUpdateTable={(value) => this.updateMcpToolsField("mcpTools", value)} />
              </Field>
            </>
          )}
          {showRegion && (
            <Field label={this.getRegionLabel(p)}>
              <span className="text-zinc-300 text-sm"> this.updateProviderField("region", e.target.value)} />
            </Field>
          )}
          {p.category === "Blockchain" && (
            <>
              {p.type !== "Ethereum" && (
                <>
                  <Field label={Setting.getLabel(i18next.t("provider:Chain"), i18next.t("provider:Chain - Tooltip"))}>
                    <span className="text-zinc-300 text-sm"> this.updateProviderField("chain", e.target.value)} />
                  </Field>
                  <Field label={this.getNetworkLabel(p)}>
                    <span className="text-zinc-300 text-sm"> this.updateProviderField("network", e.target.value)} />
                  </Field>
                </>
              )}
              {p.type === "ChainMaker" && (
                <>
                  <Field label={Setting.getLabel(i18next.t("provider:Auth type"), i18next.t("provider:Auth type - Tooltip"))}>
                    <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500" value={p.text} onChange={e => this.updateProviderField("text", e.target.value)}>
                      {["permissionedwithcert", "permissionedwithkey", "public"].map(v => <option key={v} value={v}>{v}</option>)}
                    </select>
                  </Field>
                  <div className="grid grid-cols-2 gap-4 mt-5">
                    <div>
                      <label className="text-sm text-zinc-400 mb-2 block">{Setting.getLabel(i18next.t("cert:User cert"), i18next.t("cert:User cert - Tooltip"))}</label>
                      <div className="flex gap-2 mb-2">
                        <button disabled={!p.userCert} onClick={() => { copy(p.userCert); Setting.showMessage("success", i18next.t("general:Copied to clipboard successfully")); }} className="px-3 py-1 bg-zinc-800 text-zinc-300 rounded text-xs hover:bg-zinc-700 transition-colors disabled:opacity-50">{i18next.t("general:Copy")}</button>
                        <button disabled={!p.userCert} onClick={() => { FileSaver.saveAs(new Blob([p.userCert], {type: "text/plain"}), "user_cert.pem"); }} className="px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors disabled:opacity-50">{i18next.t("general:Download")}</button>
                      </div>
                      <textarea className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white font-mono focus:outline-none focus:border-zinc-500" rows={16} value={p.userCert || ""} onChange={e => this.updateProviderField("userCert", e.target.value)} />
                    </div>
                    <div>
                      <label className="text-sm text-zinc-400 mb-2 block">{Setting.getLabel(i18next.t("cert:User key"), i18next.t("cert:User key - Tooltip"))}</label>
                      <div className="flex gap-2 mb-2">
                        <button disabled={!p.userKey} onClick={() => { copy(p.userKey); Setting.showMessage("success", i18next.t("general:Copied to clipboard successfully")); }} className="px-3 py-1 bg-zinc-800 text-zinc-300 rounded text-xs hover:bg-zinc-700 transition-colors disabled:opacity-50">{i18next.t("general:Copy")}</button>
                        <button disabled={!p.userKey} onClick={() => { FileSaver.saveAs(new Blob([p.userKey], {type: "text/plain"}), "token_jwt_key.key"); }} className="px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors disabled:opacity-50">{i18next.t("general:Download")}</button>
                      </div>
                      <textarea className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white font-mono focus:outline-none focus:border-zinc-500" rows={16} value={p.userKey || ""} onChange={e => this.updateProviderField("userKey", e.target.value)} />
                    </div>
                  </div>
                  <div className="grid grid-cols-2 gap-4 mt-5">
                    <div>
                      <label className="text-sm text-zinc-400 mb-2 block">{Setting.getLabel(i18next.t("cert:Sign cert"), i18next.t("cert:Sign cert - Tooltip"))}</label>
                      <div className="flex gap-2 mb-2">
                        <button disabled={!p.signCert} onClick={() => { copy(p.signCert); Setting.showMessage("success", i18next.t("general:Copied to clipboard successfully")); }} className="px-3 py-1 bg-zinc-800 text-zinc-300 rounded text-xs hover:bg-zinc-700 transition-colors disabled:opacity-50">{i18next.t("general:Copy")}</button>
                        <button disabled={!p.signCert} onClick={() => { FileSaver.saveAs(new Blob([p.signCert], {type: "text/plain"}), "user_cert.pem"); }} className="px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors disabled:opacity-50">{i18next.t("general:Download")}</button>
                      </div>
                      <textarea className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white font-mono focus:outline-none focus:border-zinc-500" rows={16} value={p.signCert || ""} onChange={e => this.updateProviderField("signCert", e.target.value)} />
                    </div>
                    <div>
                      <label className="text-sm text-zinc-400 mb-2 block">{Setting.getLabel(i18next.t("cert:Sign key"), i18next.t("cert:Sign key - Tooltip"))}</label>
                      <div className="flex gap-2 mb-2">
                        <button disabled={!p.signKey} onClick={() => { copy(p.signKey); Setting.showMessage("success", i18next.t("general:Copied to clipboard successfully")); }} className="px-3 py-1 bg-zinc-800 text-zinc-300 rounded text-xs hover:bg-zinc-700 transition-colors disabled:opacity-50">{i18next.t("general:Copy")}</button>
                        <button disabled={!p.signKey} onClick={() => { FileSaver.saveAs(new Blob([p.signKey], {type: "text/plain"}), "token_jwt_key.key"); }} className="px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors disabled:opacity-50">{i18next.t("general:Download")}</button>
                      </div>
                      <textarea className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white font-mono focus:outline-none focus:border-zinc-500" rows={16} value={p.signKey || ""} onChange={e => this.updateProviderField("signKey", e.target.value)} />
                    </div>
                  </div>
                </>
              )}
              {["Ethereum", "ChainMaker"].includes(p.type) && (
                <>
                  <Field label={this.getContractNameLabel(p)}>
                    <span className="text-zinc-300 text-sm"> this.updateProviderField("contractName", e.target.value)} />
                  </Field>
                  <Field label={Setting.getLabel(i18next.t("provider:Invoke method"), i18next.t("provider:Invoke method - Tooltip"))}>
                    <span className="text-zinc-300 text-sm"> this.updateProviderField("contractMethod", e.target.value)} />
                  </Field>
                </>
              )}
              <Field label={Setting.getLabel(i18next.t("provider:Browser URL"), i18next.t("provider:Browser URL - Tooltip"))}>
                <span className="text-zinc-300 text-sm"> this.updateProviderField("browserUrl", e.target.value)} prefix={<Link2 className="w-4 h-4" />} placeholder={p.type === "ChainMaker" ? "https://explorer-testnet.chainmaker.org.cn/chainmaker_testnet_chain/block/{bh}" : ""} />
              </Field>
            </>
          )}
          {this.isTemperatureEnabled(p) && (
            <Field label={Setting.getLabel(i18next.t("provider:Temperature"), i18next.t("provider:Temperature - Tooltip"))}>
              <this.InputSlider min={0} max={["Alibaba Cloud", "Gemini", "OpenAI", "OpenRouter", "Baichuan", "DeepSeek", "StepFun", "Tencent Cloud", "Mistral", "Yi", "Ollama", "Writer"].includes(p.type) ? 2 : 1} step={0.01} value={p.temperature} disabled={isRemote} onChange={v => this.updateProviderField("temperature", v)} />
            </Field>
          )}
          {this.isTopPEnabled(p) && (
            <Field label={Setting.getLabel(i18next.t("provider:Top P"), i18next.t("provider:Top P - Tooltip"))}>
              <this.InputSlider min={0} max={1.0} step={0.01} value={p.topP} disabled={isRemote} onChange={v => this.updateProviderField("topP", v)} />
            </Field>
          )}
          {p.category === "Model" && p.type === "Gemini" && (
            <Field label={Setting.getLabel(i18next.t("provider:Top K"), i18next.t("provider:Top K - Tooltip"))}>
              <this.InputSlider min={1} max={6} step={1} value={p.topK} disabled={isRemote} onChange={v => this.updateProviderField("topK", v)} />
            </Field>
          )}
          {p.category === "Model" && p.type === "OpenAI" && !["o1", "o1-pro", "o3", "o3-mini", "o4-mini"].includes(p.subType) && (
            <>
              <Field label={Setting.getLabel(i18next.t("provider:Presence penalty"), i18next.t("provider:Presence penalty - Tooltip"))}>
                <this.InputSlider min={p.type === "OpenAI" ? -2 : 1} max={2} step={0.01} value={p.presencePenalty} disabled={isRemote} onChange={v => this.updateProviderField("presencePenalty", v)} />
              </Field>
              <Field label={Setting.getLabel(i18next.t("provider:Frequency penalty"), i18next.t("provider:Frequency penalty - Tooltip"))}>
                <this.InputSlider min={-2} max={2} step={0.01} value={p.frequencyPenalty} disabled={isRemote} onChange={v => this.updateProviderField("frequencyPenalty", v)} />
              </Field>
            </>
          )}
          {(p.category === "Model" || p.category === "Embedding") && p.type === "Azure" && (
            <Field label={Setting.getLabel(i18next.t("provider:API version"), i18next.t("provider:API version - Tooltip"))}>
              <span className="text-zinc-300 text-sm"> this.updateProviderField("apiVersion", e.target.value)} />
            </Field>
          )}
          <ModelTestWidget provider={p} originalProvider={this.state.originalProvider} account={this.props.account} />
          <EmbedTestWidget provider={p} originalProvider={this.state.originalProvider} account={this.props.account} onUpdateProvider={this.updateProviderField.bind(this)} />
          <TtsTestWidget provider={p} originalProvider={this.state.originalProvider} account={this.props.account} onUpdateProvider={this.updateProviderField.bind(this)} />
          <TestScanWidget provider={p} originalProvider={this.state.originalProvider} account={this.props.account} onUpdateProvider={this.updateProviderField.bind(this)} />
          {p.category === "Model" && (
            <Field label={Setting.getLabel(i18next.t("provider:Provider key"), i18next.t("provider:Provider key - Tooltip"))}>
              <input type="password" disabled={!Setting.isAdminUser(this.props.account)} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.providerKey || ""} onChange={e => this.updateProviderField("providerKey", e.target.value)} />
            </Field>
          )}
          {p.category === "Private Cloud" && p.type === "Kubernetes" && (
            <Field label={Setting.getLabel(i18next.t("provider:Config text"), i18next.t("provider:Config text - Tooltip"))}>
              <Editor value={p.configText} lang="yaml" fillHeight dark readOnly={isRemote || !Setting.isAdminUser(this.props.account)} onChange={value => this.updateProviderField("configText", value)} />
            </Field>
          )}
          {p.category === "Scan" && (
            <>
              <Field label={Setting.getLabel(i18next.t("scan:Result summary"), i18next.t("scan:Result summary - Tooltip"))}>
                <span className="text-zinc-300 text-sm"> {}} />
              </Field>
              <Field label={Setting.getLabel(i18next.t("scan:Runner"), i18next.t("scan:Runner - Tooltip"))}>
                <span className="text-zinc-300 text-sm"> {}} />
              </Field>
              <Field label={Setting.getLabel(i18next.t("general:Error"), i18next.t("scan:Error - Tooltip"))}>
                <textarea disabled className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none disabled:opacity-50" rows={4} value={p.errorText || ""} />
              </Field>
            </>
          )}
          <Field label={this.getProviderUrlLabel(p)}>
            <span className="text-zinc-300 text-sm"> this.updateProviderField("providerUrl", e.target.value)} prefix={<Link2 className="w-4 h-4" />} />
          </Field>
          <Field label={Setting.getLabel(i18next.t("store:Is default"), i18next.t("store:Is default - Tooltip"))}>
            <label className="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" disabled={isRemote} checked={p.isDefault} onChange={e => this.updateProviderField("isDefault", e.target.checked)} className="sr-only peer" />
              <div className="w-9 h-5 bg-zinc-700 rounded-full peer peer-checked:bg-white peer-checked:after:translate-x-full after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-zinc-900 after:rounded-full after:h-4 after:w-4 after:transition-all" />
            </label>
          </Field>
          <Field label={Setting.getLabel(i18next.t("provider:Is remote"), i18next.t("provider:Is remote - Tooltip"))}>
            <label className="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" disabled checked={p.isRemote} className="sr-only peer" />
              <div className="w-9 h-5 bg-zinc-700 rounded-full peer peer-checked:bg-white peer-checked:after:translate-x-full after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-zinc-900 after:rounded-full after:h-4 after:w-4 after:transition-all" />
            </label>
          </Field>
          <Field label={Setting.getLabel(i18next.t("general:State"), i18next.t("general:State - Tooltip"))}>
            <select disabled={isRemote} className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={p.state} onChange={e => this.updateProviderField("state", e.target.value)}>
              <option value="Active">{i18next.t("general:Active")}</option>
              <option value="Inactive">{i18next.t("general:Inactive")}</option>
            </select>
          </Field>
        </div>
      </div>
    );
  }

  refreshMcpTools() {
    this.setState({refreshButtonLoading: true});
    const provider = Setting.deepCopy(this.state.provider);
    provider.mcpTools = [];
    ProviderBackend.refreshMcpTools(provider)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully saved"));
          this.setState({provider: res.data}, () => this.submitProviderEdit(false));
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
          this.setState({provider});
        }
      })
      .catch((error) => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
        this.setState({provider});
      })
      .finally(() => this.setState({refreshButtonLoading: false}));
  }

  submitProviderEdit(exitAfterSave) {
    const provider = Setting.deepCopy(this.state.provider);
    ProviderBackend.updateProvider(this.state.provider.owner, this.state.providerName, provider)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({providerName: this.state.provider.name, isNewProvider: false});
            if (exitAfterSave) {this.props.history.push("/providers");} else {this.props.history.push(`/providers/${this.state.provider.name}`);}
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
            this.updateProviderField("name", this.state.providerName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`));
  }

  cancelProviderEdit() {
    if (this.state.isNewProvider) {
      ProviderBackend.deleteProvider(this.state.provider)
        .then((res) => {
          if (res.status === "ok") {Setting.showMessage("success", i18next.t("general:Cancelled successfully")); this.props.history.push("/providers");}
          else {Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);}
        })
        .catch(error => Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`));
    } else {
      this.props.history.push("/providers");
    }
  }

  render() {
    const isRemote = this.state.provider?.isRemote;
    return (
      <div>
        {this.state.provider !== null ? this.renderProvider() : null}
        {!isRemote && (
          <div className="mt-5 ml-10 flex gap-3">
            <button onClick={() => this.submitProviderEdit(false)} className="px-6 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Save")}</button>
            <button onClick={() => this.submitProviderEdit(true)} className="px-6 py-2 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">{i18next.t("general:Save & Exit")}</button>
            {this.state.isNewProvider && <button onClick={() => this.cancelProviderEdit()} className="px-6 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors">{i18next.t("general:Cancel")}</button>}
          </div>
        )}
      </div>
    );
  }
}

export default ProviderEditPage;
