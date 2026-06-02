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
import {ArrowLeft, List, Database, ChevronDown, ChevronRight, Loader2} from "lucide-react";
import i18next from "i18next";
import * as Setting from "./Setting";
import * as FileBackend from "./backend/FileBackend";
import * as VectorBackend from "./backend/VectorBackend";
import "katex/dist/katex.min.css";
import ReactMarkdown from "react-markdown";
import remarkMath from "remark-math";
import rehypeKatex from "rehype-katex";

class FileViewPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      fileName: decodeURIComponent(props.match.params.fileName),
      file: null,
      vectors: [],
      loading: true,
      expandedItems: {},
      pagination: {
        current: 1,
        pageSize: 10,
        pageSizeOptions: ["10", "20", "50", "100"],
      },
    };
    this.listScrollRef = null;
  }

  UNSAFE_componentWillMount() {
    this.getFile();
  }

  getFile = () => {
    const {fileName} = this.state;
    FileBackend.getFile("admin", fileName).then((res) => {
      if (res.status === "ok") {
        this.setState({file: res.data, fileName: fileName});
        this.getVectors(res.data);
      } else {
        Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        this.setState({loading: false});
      }
    });
  };

  getVectors = (file) => {
    const objectKey = file.name.startsWith(`${file.store}_`)
      ? file.name.substring(file.store.length + 1)
      : file.name;
    VectorBackend
      .getVectors("admin", file.store, "", "", "file", objectKey, "index", "asc")
      .then((res) => {
        if (res.status === "ok") {
          this.setState({vectors: res.data || [], loading: false});
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")} vectors: ${res.msg}`);
          this.setState({loading: false});
        }
      });
  };

  handlePageChange = (page) => {
    this.setState({
      pagination: {...this.state.pagination, current: page},
    }, () => {
      if (this.listScrollRef) {
        this.listScrollRef.scrollTop = 0;
      }
    });
  };

  toggleExpanded = (itemName) => {
    this.setState(prev => ({
      expandedItems: {...prev.expandedItems, [itemName]: !prev.expandedItems[itemName]},
    }));
  };

  renderHeader = () => {
    const {file, fileName} = this.state;
    return (
      <div className="flex items-center justify-between px-6 h-16 border-b border-zinc-800 bg-black shrink-0">
        <div className="flex items-center gap-4">
          <button onClick={() => this.props.history.goBack()} className="text-zinc-400 hover:text-white transition-colors">
            <ArrowLeft className="w-5 h-5" />
          </button>
          <div>
            <h1 className="text-base font-medium text-white leading-tight">{file?.filename || fileName}</h1>
            <p className="text-xs text-zinc-500 flex items-center gap-1">
               {file?.store || "Universal Store"}
            </p>
          </div>
        </div>
      </div>
    );
  };

  renderRightSidebar = () => {
    const {file, vectors} = this.state;
    if (!file) {return null;}

    const InfoItem = ({label, value}) => (
      <div className="mb-3">
        <div className="text-[11px] uppercase tracking-wider text-zinc-500 mb-0.5">{label}</div>
        <div className="text-sm font-semibold text-white break-all">{value}</div>
      </div>
    );

    return (
      <div className="p-6 border-l border-zinc-800 h-[calc(100vh-64px)] overflow-y-auto bg-black">
        <div className="mb-8">
          <h4 className="text-sm font-medium text-white mb-4">DOCUMENTATION INFO</h4>
          <InfoItem label="Original File Name" value={file.filename} />
          <InfoItem label="File Size" value={Setting.getFormattedSize(file.size)} />
          <InfoItem label="Upload Date" value={Setting.getFormattedDate(file.createdTime)} />
          <InfoItem label="Storage Provider" value={<span className="px-2 py-0.5 bg-blue-500/20 text-blue-400 rounded text-xs">{file.storageProvider}</span>} />
        </div>
        <div className="h-px bg-zinc-800 mb-8" />
        <div>
          <h4 className="text-sm font-medium text-white mb-4">TECHNICAL SPECS</h4>
          <InfoItem label="Store Name" value={file.store} />
          <InfoItem label="Total Segments" value={`${vectors.length} items`} />
          <InfoItem label="Token Count" value={`${file.tokenCount} tokens`} />
          <InfoItem label="Vector Dimension" value={vectors.length > 0 ? vectors[0].dimension : "-"} />
        </div>
      </div>
    );
  };

  renderVectorList = () => {
    const {vectors, pagination} = this.state;
    const {current, pageSize} = pagination;
    const startIndex = (current - 1) * pageSize;
    const endIndex = startIndex + pageSize;
    const currentData = vectors.slice(startIndex, endIndex);
    const totalPages = Math.ceil(vectors.length / pageSize);

    return (
      <div className="h-full flex flex-col">
        <div className="flex items-center justify-between px-6 py-3 border-b border-zinc-800 bg-black shrink-0">
          <h2 className="text-base font-semibold text-white">Segments ({vectors.length})</h2>
          <div className="flex items-center gap-2 text-xs text-zinc-400">
            <span>{startIndex + 1}-{Math.min(endIndex, vectors.length)} of {vectors.length}</span>
            <div className="flex gap-1">
              {Array.from({length: Math.min(totalPages, 5)}, (_, i) => i + 1).map(page => (
                <button
                  key={page}
                  onClick={() => this.handlePageChange(page)}
                  className={`w-7 h-7 rounded text-xs ${page === current ? "bg-white text-black font-medium" : "bg-zinc-800 text-zinc-400 hover:bg-zinc-700"} transition-colors`}
                >
                  {page}
                </button>
              ))}
            </div>
          </div>
        </div>
        <div ref={(el) => this.listScrollRef = el} className="flex-1 overflow-y-auto p-6 space-y-4">
          {currentData.map((item) => (
            <div key={item.name} className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-5">
              <div className="flex justify-between items-start mb-3">
                <div>
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="text-base font-semibold text-blue-400">#{item.index}</span>
                    <span className="text-xs text-zinc-500">{item.tokenCount} tokens</span>
                    {item.score > 0 && <span className="px-2 py-0.5 bg-yellow-500/20 text-yellow-400 rounded text-xs">Score: {item.score.toFixed(4)}</span>}
                  </div>
                  <div className="flex gap-2 mt-1">
                    <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">{item.provider}</span>
                    <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">{item.price} {item.currency}</span>
                  </div>
                </div>
                <span className="flex items-center gap-1 text-xs text-green-400">
                  <span className="w-1.5 h-1.5 bg-green-400 rounded-full" />
                  Enabled
                </span>
              </div>
              <div className="pl-4 mb-3 text-sm text-zinc-300 leading-relaxed">
                <ReactMarkdown remarkPlugins={[remarkMath]} rehypePlugins={[rehypeKatex]} components={{p: ({node, ...props}) => <p className="m-0" {...props} />}}>
                  {item.text || ""}
                </ReactMarkdown>
              </div>
              <div className="pl-4">
                <button onClick={() => this.toggleExpanded(item.name)} className="flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-300 transition-colors">
                  {this.state.expandedItems[item.name] ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
                  Show Data & Details
                </button>
                {this.state.expandedItems[item.name] && (
                  <div className="mt-3 bg-zinc-900 border border-zinc-800 rounded-lg p-4">
                    <div className="grid grid-cols-2 gap-3 text-xs mb-3">
                      <div><span className="text-zinc-500">UUID: </span><span className="text-zinc-300">{item.name}</span></div>
                      <div><span className="text-zinc-500">Display Name: </span><span className="text-zinc-300">{item.displayName}</span></div>
                      <div><span className="text-zinc-500">Owner: </span><span className="text-zinc-300">{item.owner}</span></div>
                      <div><span className="text-zinc-500">Store: </span><span className="text-zinc-300">{item.store}</span></div>
                    </div>
                    <div className="text-xs font-medium text-zinc-300 mb-1 flex items-center gap-1">
                      <Database className="w-3 h-3" /> Vector Data ({item.dimension} dim)
                    </div>
                    <div className="max-h-[120px] overflow-y-auto bg-black border border-zinc-800 rounded p-2 text-[11px] font-mono text-zinc-500 break-all">
                      [{item.data ? item.data.join(", ") : ""}]
                    </div>
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>
      </div>
    );
  };

  render() {
    const {loading} = this.state;
    if (loading) {
      return (
        <div className="flex items-center justify-center h-screen">
          <Loader2 className="w-8 h-8 text-zinc-500 animate-spin" />
        </div>
      );
    }

    return (
      <div className="h-screen flex flex-col overflow-hidden bg-black">
        {this.renderHeader()}
        <div className="flex-1 overflow-hidden">
          <div className="grid grid-cols-1 lg:grid-cols-[1fr,300px] h-full">
            <div className="h-full">{this.renderVectorList()}</div>
            <div className="h-full hidden lg:block">{this.renderRightSidebar()}</div>
          </div>
        </div>
      </div>
    );
  }
}

export default FileViewPage;
