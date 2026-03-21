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
import {withRouter} from "react-router-dom";
import {
  Upload, Download, Trash2, FolderPlus, FilePlus, ChevronRight, ChevronDown,
  File, Folder, FolderOpen, AlertTriangle, Search, Info, Shield, Loader2,
} from "lucide-react";
import moment from "moment";
import * as Setting from "./Setting";
import * as TreeFileBackend from "./backend/TreeFileBackend";
import DocViewer, {DocViewerRenderers} from "@cyntler/react-doc-viewer";
import FileViewer from "react-file-viewer";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import remarkFrontmatter from "remark-frontmatter";
import i18next from "i18next";
import * as PermissionBackend from "./backend/PermissionBackend";
import * as PermissionUtil from "./PermissionUtil";
import * as Conf from "./Conf";
import FileTable from "./table/FileTable";
import Editor from "./common/Editor";

class FileTree extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      expandedKeys: ["0-0", "0-0-0", "0-0-0-0"],
      checkedKeys: [],
      checkedFiles: [],
      selectedKeys: [],
      selectedFile: null,
      loading: false,
      text: null,
      newFolder: null,
      permissions: null,
      permissionMap: null,
      searchValue: "",
      isUploadFileModalVisible: false,
      uploadFileType: null,
      file: null,
      info: null,
      showNewFolderInput: null,
      expandedTreeKeys: {},
    };

    this.filePane = React.createRef();
    this.uploadedFileIdMap = {};
    this.fileInputRef = React.createRef();
    this.folderInputRef = React.createRef();
    this.handleKeyDown = this.handleKeyDown.bind(this);
  }

  componentDidMount() {
    document.addEventListener("keydown", this.handleKeyDown);
    this.applyInitialSelection();
  }

  componentDidUpdate(prevProps) {
    if (!prevProps.store && this.props.store) {
      this.applyInitialSelection();
    }
  }

  componentWillUnmount() {
    document.removeEventListener("keydown", this.handleKeyDown);
  }

  handleKeyDown(e) {
    if (e.key === "Delete" && this.state.checkedFiles.length > 0) {
      e.preventDefault();
      this.batchDeleteFiles();
    }
  }

  UNSAFE_componentWillMount() {
    this.getPermissions();
  }

  batchDeleteFiles() {
    if (window.confirm(i18next.t("store:Are you sure you want to delete the selected items?"))) {
      this.state.checkedFiles.forEach(file => {
        this.deleteFile(file, file.isLeaf);
      });
      this.setState({checkedKeys: [], checkedFiles: []});
    }
  }

  getPermissionMap(permissions) {
    const permissionMap = {};
    permissions.forEach((permission) => {
      if (permissionMap[permission.resources[0]] === undefined) {
        permissionMap[permission.resources[0]] = [];
      }
      permissionMap[permission.resources[0]].push(permission);
    });
    return permissionMap;
  }

  getPermissions() {
    PermissionBackend.getPermissions(Conf.AuthConfig.organizationName)
      .then((res) => {
        if (res.status === "ok") {
          const permissions = res.data.filter(permission => (permission.domains[0] === this.props.store.name) && permission.users.length !== 0);
          this.setState({
            permissions: permissions,
            permissionMap: this.getPermissionMap(permissions),
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  updateStore(store) {
    this.props.onUpdateStore(store);
  }

  checkUploadFile(info) {
    if (Conf.EnableExtraPages) {
      for (let i = 0; i < info.fileList.length; i++) {
        const filename = info.fileList[i].name;
        if (this.getCacheApp(filename) === "" && filename.endsWith(".txt")) {
          return true;
        }
      }
    }
    return false;
  }

  renderUploadFileModal() {
    if (!this.state.isUploadFileModalVisible) {return null;}

    const uploadTypes = ["ECG", "Impedance", "Other"];
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60" onClick={() => this.setState({isUploadFileModalVisible: false})}>
        <div className="bg-zinc-900 border border-zinc-800 rounded-lg p-6 w-[360px]" onClick={(e) => e.stopPropagation()}>
          <div className="flex items-center gap-2 mb-4">
            <Info className="w-5 h-5 text-blue-400" />
            <h3 className="text-sm font-medium text-white">{i18next.t("store:Please choose the type of your data")}</h3>
          </div>
          <div className="flex gap-2">
            {uploadTypes.map(type => (
              <button
                key={type}
                onClick={() => {
                  this.setState({uploadFileType: type, isUploadFileModalVisible: false});
                  const newInfo = Setting.deepCopy(this.state.info);
                  if (type !== "Other") {
                    for (let i = 0; i < newInfo.fileList.length; i++) {
                      const filename = newInfo.fileList[i].name;
                      if (this.getCacheApp(filename) === "" && filename.endsWith(".txt")) {
                        newInfo.fileList[i].name = `${type}_${newInfo.fileList[i].name}`;
                      }
                    }
                  }
                  this.uploadFile(this.state.file, newInfo);
                }}
                className="px-4 py-2 bg-zinc-800 text-zinc-300 rounded text-sm hover:bg-zinc-700 transition-colors"
              >
                {type === "Other" ? i18next.t("med:Other") : type}
              </button>
            ))}
          </div>
        </div>
      </div>
    );
  }

  uploadFiles(file, files) {
    const info = {
      fileList: Array.from(files).map(file => {
        file.uid = Date.now() + Math.floor(Math.random() * 1000);
        return {name: file.name, originFileObj: file};
      }),
    };
    this.uploadFile(file, info);
  }

  uploadFile(file, info) {
    const storeId = `${this.props.store.owner}/${this.props.store.name}`;
    const promises = [];
    info.fileList.forEach((uploadedFile) => {
      if (this.uploadedFileIdMap[uploadedFile.originFileObj.uid] === 1) {
        return;
      }
      this.uploadedFileIdMap[uploadedFile.originFileObj.uid] = 1;
      promises.push(TreeFileBackend.addFile(storeId, file.key, true, uploadedFile.name, uploadedFile.originFileObj));
    });

    Promise.all(promises)
      .then((values) => {
        if (promises.length === 0) {return;}
        values.forEach((res) => {
          if (res.status !== "ok") {
            Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
          }
        });
        Setting.showMessage("success", i18next.t("general:Successfully uploaded"));
        this.props.onRefresh();
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  addFile(file, newFolder) {
    const storeId = `${this.props.store.owner}/${this.props.store.name}`;
    TreeFileBackend.addFile(storeId, file.key, false, newFolder, null)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.onRefresh();
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  deleteFile(file, isLeaf) {
    const storeId = `${this.props.store.owner}/${this.props.store.name}`;
    TreeFileBackend.deleteFile(storeId, file.key, isLeaf)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data === true) {
            Setting.showMessage("success", i18next.t("general:Successfully deleted"));
            this.props.onRefresh();
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${error}`);
      });
  }

  renderPermission(permission, isReadable) {
    if (!isReadable) {
      const userId = `${this.props.account.owner}/${this.props.account.name}`;
      if (!permission.users.includes(userId)) {
        return null;
      }
    }
    return (
      <span key={permission.name} onClick={(e) => {
        Setting.openLink(Setting.getMyProfileUrl(this.props.account).replace("/account", `/permissions/${permission.owner}/${permission.name}`));
        e.stopPropagation();
      }}>
        {permission.users.map(user => {
          const username = user.split("/")[1];
          return <span key={username}>{Setting.getTag(username, permission.actions[0], permission.state)}</span>;
        })}
      </span>
    );
  }

  renderPermissions(permissions, isReadable) {
    if (permissions === undefined) {return null;}
    return permissions.map(permission => this.renderPermission(permission, isReadable)).filter(p => p !== null);
  }

  isActionIncluded(action1, action2) {
    if (action1 === "Read") {return true;}
    if (action1 === "Write" && action2 !== "Read") {return true;}
    if (action1 === "Admin" && action2 === "Admin") {return true;}
    return false;
  }

  isFileOk(file, action) {
    if (Setting.isLocalAndStoreAdminUser(this.props.account)) {return true;}
    if (this.state.permissionMap === null) {return false;}
    const permissions = this.state.permissionMap[file.key];
    if (permissions !== undefined) {
      for (let i = 0; i < permissions.length; i++) {
        const permission = permissions[i];
        const userId = `${this.props.account.owner}/${this.props.account.name}`;
        if (permission.state === "Approved" && permission.isEnabled === true && permission.resources[0] === file.key && permission.users.includes(userId) && this.isActionIncluded(action, permission.actions[0])) {
          return true;
        }
      }
    }
    if (file.parent !== undefined) {return this.isFileOk(file.parent, action);}
    return false;
  }

  isFileReadable(file) {
    if (Setting.isLocalAndStoreAdminUser(this.props.account)) {return true;}
    return this.isFileOk(file, "Read");
  }

  isFileWritable(file) {
    if (Setting.isLocalAndStoreAdminUser(this.props.account)) {return true;}
    return this.isFileOk(file, "Write");
  }

  isFileAdmin(file) {
    if (Setting.isLocalAndStoreAdminUser(this.props.account)) {return true;}
    return this.isFileOk(file, "Admin");
  }

  getCacheApp(filename) {
    if (!filename.startsWith("ECG_") && !filename.startsWith("EEG_") && !filename.startsWith("Impedance_")) {
      return "";
    }
    return filename;
  }

  findFileNodeByKey(file, targetKey) {
    if (!file) {return null;}
    if (file.key === targetKey) {return file;}
    if (!file.children?.length) {return null;}
    for (const child of file.children) {
      const found = this.findFileNodeByKey(child, targetKey);
      if (found) {return found;}
    }
    return null;
  }

  applyInitialSelection() {
    const {store, initialFileKey} = this.props;
    if (!store?.fileTree || !initialFileKey) {return;}
    if (this.state.selectedKeys.length !== 0 || this.state.selectedFile !== null) {return;}
    const targetKey = initialFileKey.replace(/^\/+/, "").replace(/\/+$/, "");
    const file = this.findFileNodeByKey(store.fileTree, targetKey);
    if (!file?.isLeaf) {return;}
    this.setState({checkedKeys: [], checkedFiles: [], selectedKeys: [file.key], selectedFile: file});
    const ext = Setting.getExtFromPath(file.key);
    if (ext && file.url && !this.isExtForDocViewer(ext) && !this.isExtForFileViewer(ext)) {
      this.setState({loading: true});
      fetch(file.url, {method: "GET", credentials: "include"})
        .then(res => res.text())
        .then(res => this.setState({text: res, loading: false}))
        .catch(() => this.setState({loading: false}));
    }
  }

  handleFileSelect(file) {
    if (!this.isFileReadable(file)) {
      Setting.showMessage("error", i18next.t("store:Sorry, you are unauthorized to access this file or folder"));
      return;
    }

    const selectedKeys = [file.key];
    const fetchFile = () => {
      const path = file.key;
      const ext = Setting.getExtFromPath(path);
      if (ext !== "") {
        const url = file.url;
        if (!this.isExtForDocViewer(ext) && !this.isExtForFileViewer(ext)) {
          this.setState({loading: true});
          fetch(url, {method: "GET", credentials: "include"})
            .then(res => res.text())
            .then(res => this.setState({text: res, loading: false}));
        }
      }
    };

    const filename = file.title;
    if (this.getCacheApp(filename) !== "") {
      TreeFileBackend.activateFile(file.key, filename)
        .then((res) => {
          if (res.status === "ok" && res.data === true) {
            fetchFile();
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to activate")}: ${res.msg || ""}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to activate")}: ${error}`);
        });
    } else {
      fetchFile();
    }

    this.setState({checkedKeys: [], checkedFiles: [], selectedKeys: selectedKeys, selectedFile: file});
  }

  toggleTreeExpand(key) {
    this.setState(prev => ({
      expandedTreeKeys: {...prev.expandedTreeKeys, [key]: !prev.expandedTreeKeys[key]},
    }));
  }

  renderTreeNode(file, depth = 0) {
    if (!file) {return null;}
    const isReadable = this.isFileReadable(file);
    const isWritable = this.isFileWritable(file);
    const isAdmin = this.isFileAdmin(file);
    const isSelected = this.state.selectedKeys.includes(file.key);
    const isChecked = this.state.checkedKeys.includes(file.key);
    const isExpanded = this.state.expandedTreeKeys[file.key] !== false;
    const opacity = (!isReadable && !isWritable && !isAdmin) ? "opacity-40" : "";

    if (file.isLeaf) {
      return (
        <div key={file.key}>
          <div
            className={`flex items-center gap-1.5 py-1 px-2 text-sm cursor-pointer group ${isSelected ? "bg-zinc-800 text-white" : "text-zinc-300 hover:bg-zinc-800/50"} ${opacity}`}
            style={{paddingLeft: `${depth * 16 + 8}px`}}
            onClick={() => this.handleFileSelect(file)}
          >
            <input
              type="checkbox" checked={isChecked}
              onChange={(e) => {
                e.stopPropagation();
                if (e.target.checked) {
                  this.setState({
                    checkedKeys: [...this.state.checkedKeys, file.key],
                    checkedFiles: [...this.state.checkedFiles, file],
                    selectedKeys: [], selectedFile: null,
                  });
                } else {
                  this.setState({
                    checkedKeys: this.state.checkedKeys.filter(k => k !== file.key),
                    checkedFiles: this.state.checkedFiles.filter(f => f.key !== file.key),
                  });
                }
              }}
              className="w-3 h-3 rounded bg-zinc-800 border-zinc-700 mr-1"
            />
            <File className="w-3.5 h-3.5 text-zinc-500 shrink-0" />
            <span className="truncate">{file.title} ({Setting.getFriendlyFileSize(file.size)})</span>
            <div className="ml-auto flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
              {isReadable && (
                <button onClick={(e) => { e.stopPropagation(); Setting.openLink(file.url); }} className="p-0.5 text-zinc-500 hover:text-white" title={i18next.t("general:Download")}>
                  <Download className="w-3.5 h-3.5" />
                </button>
              )}
              {isWritable && (
                <button onClick={(e) => {
                  e.stopPropagation();
                  if (window.confirm(`${i18next.t("general:Sure to delete")}: ${file.title} ?`)) {
                    this.deleteFile(file, true);
                  }
                }} className="p-0.5 text-zinc-500 hover:text-red-400" title={i18next.t("general:Delete")}>
                  <Trash2 className="w-3.5 h-3.5" />
                </button>
              )}
              <button onClick={(e) => {
                e.stopPropagation();
                PermissionUtil.addPermission(this.props.account, this.props.store, isAdmin, file);
              }} className="p-0.5 text-zinc-500 hover:text-white" title={isAdmin ? i18next.t("store:Add Permission") : i18next.t("store:Apply for Permission")}>
                <Shield className="w-3.5 h-3.5" />
              </button>
            </div>
            {this.state.permissionMap !== null && this.renderPermissions(this.state.permissionMap[file.key], isReadable)}
          </div>
        </div>
      );
    }

    // Folder node
    return (
      <div key={file.key}>
        <div
          className={`flex items-center gap-1.5 py-1 px-2 text-sm cursor-pointer group ${isSelected ? "bg-zinc-800 text-white" : "text-zinc-300 hover:bg-zinc-800/50"} ${opacity}`}
          style={{paddingLeft: `${depth * 16 + 8}px`}}
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault();
            const files = Array.from(e.dataTransfer.files || []);
            if (files.length > 0) {this.uploadFiles(file, files);}
          }}
          onClick={() => this.toggleTreeExpand(file.key)}
        >
          <input
            type="checkbox" checked={isChecked}
            onChange={(e) => {
              e.stopPropagation();
              if (e.target.checked) {
                this.setState({
                  checkedKeys: [...this.state.checkedKeys, file.key],
                  checkedFiles: [...this.state.checkedFiles, file],
                  selectedKeys: [], selectedFile: null,
                });
              } else {
                this.setState({
                  checkedKeys: this.state.checkedKeys.filter(k => k !== file.key),
                  checkedFiles: this.state.checkedFiles.filter(f => f.key !== file.key),
                });
              }
            }}
            className="w-3 h-3 rounded bg-zinc-800 border-zinc-700 mr-1"
          />
          {isExpanded ? <ChevronDown className="w-3.5 h-3.5 text-zinc-500 shrink-0" /> : <ChevronRight className="w-3.5 h-3.5 text-zinc-500 shrink-0" />}
          {isExpanded ? <FolderOpen className="w-3.5 h-3.5 text-zinc-500 shrink-0" /> : <Folder className="w-3.5 h-3.5 text-zinc-500 shrink-0" />}
          <span className="truncate">{file.title}</span>
          <div className="ml-auto flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
            {isWritable && (
              <>
                <button onClick={(e) => {
                  e.stopPropagation();
                  this.setState({showNewFolderInput: file.key});
                }} className="p-0.5 text-zinc-500 hover:text-white" title={i18next.t("store:New folder")}>
                  <FolderPlus className="w-3.5 h-3.5" />
                </button>
                <button onClick={(e) => {
                  e.stopPropagation();
                  this.fileInputRef.current.dataset.parentKey = file.key;
                  this.fileInputRef.current.click();
                }} className="p-0.5 text-zinc-500 hover:text-white" title={i18next.t("store:Upload file")}>
                  
                </button>
                {file.key !== "/" && (
                  <button onClick={(e) => {
                    e.stopPropagation();
                    if (window.confirm(`${i18next.t("general:Sure to delete")}: ${file.title} ?`)) {
                      this.deleteFile(file, false);
                    }
                  }} className="p-0.5 text-zinc-500 hover:text-red-400" title={i18next.t("general:Delete")}>
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                )}
              </>
            )}
            <button onClick={(e) => {
              e.stopPropagation();
              PermissionUtil.addPermission(this.props.account, this.props.store, isAdmin, file);
            }} className="p-0.5 text-zinc-500 hover:text-white" title={isAdmin ? i18next.t("store:Add Permission") : i18next.t("store:Apply for Permission")}>
              <Shield className="w-3.5 h-3.5" />
            </button>
          </div>
          {this.state.permissionMap !== null && this.renderPermissions(this.state.permissionMap[file.key], isReadable)}
        </div>
        {this.state.showNewFolderInput === file.key && (
          <div className="flex items-center gap-1 py-1" style={{paddingLeft: `${(depth + 1) * 16 + 8}px`}}>
            <input
              autoFocus
              className="px-2 py-1 bg-zinc-800 border border-zinc-700 rounded text-xs text-white w-32 focus:outline-none focus:border-zinc-500"
              placeholder={i18next.t("store:New folder")}
              value={this.state.newFolder || ""}
              onChange={e => this.setState({newFolder: e.target.value})}
              onKeyDown={e => {
                if (e.key === "Enter" && this.state.newFolder) {
                  this.addFile(file, this.state.newFolder);
                  this.setState({showNewFolderInput: null, newFolder: null});
                }
                if (e.key === "Escape") {this.setState({showNewFolderInput: null});}
              }}
            />
            <button onClick={() => {
              if (this.state.newFolder) {
                this.addFile(file, this.state.newFolder);
                this.setState({showNewFolderInput: null, newFolder: null});
              }
            }} className="px-2 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200">OK</button>
          </div>
        )}
        {isExpanded && file.children?.map(child => this.renderTreeNode(child, depth + 1))}
      </div>
    );
  }

  renderTree(store) {
    let fileTree = Setting.getTreeWithParents(store.fileTree);
    if (this.state.searchValue !== "") {
      fileTree = Setting.getTreeWithSearch(fileTree, this.state.searchValue);
    }
    return (
      <div className="overflow-y-auto" style={{height: "calc(100vh - 220px)"}}>
        {this.renderTreeNode(fileTree)}
      </div>
    );
  }

  isExtForDocViewer(ext) {
    return ["bmp", "jpg", "jpeg", "png", "tiff", "doc", "docx", "ppt", "pptx", "xls", "xlsx", "pdf", "csv"].includes(ext);
  }

  isExtForFileViewer(ext) {
    return ["png", "jpg", "jpeg", "gif", "bmp", "pdf", "xlsx", "docx", "mp4", "webm", "mp3"].includes(ext);
  }

  isExtForMarkdownViewer(ext) {
    return ["md"].includes(ext);
  }

  renderFileViewer(store) {
    if (this.state.checkedFiles.length !== 0) {
      const outerFile = {children: this.state.checkedFiles};
      return <FileTable account={this.props.account} store={this.props.store} onRefresh={() => this.props.onRefresh()} file={outerFile} isCheckMode={true} />;
    }

    if (this.state.selectedKeys.length === 0) {return null;}
    const file = this.state.selectedFile;
    if (file === null) {return null;}

    const path = this.state.selectedKeys[0];
    const filename = path.split("/").pop();

    if (!file.isLeaf) {
      return <FileTable account={this.props.account} store={this.props.store} onRefresh={() => this.props.onRefresh()} file={file} isCheckMode={false} />;
    }

    if (!filename.includes(".")) {
      return (
        <div className="flex items-center justify-center text-zinc-500 text-sm" style={{height: this.getEditorHeightCss()}}>
          {i18next.t("general:No data")}
        </div>
      );
    }

    const ext = Setting.getExtFromPath(path);
    const url = this.state.selectedFile.url;
    const app = this.getCacheApp(filename);

    if (app !== "") {
      return <iframe key={path} title={app} src={`${Conf.AppUrl}${app}`} width="100%" height="100%" />;
    } else if (this.isExtForDocViewer(ext)) {
      return (
        <DocViewer
          key={path}
          style={{height: this.getEditorHeightCss(), border: "1px solid rgb(39,39,42)", borderRadius: "6px"}}
          pluginRenderers={DocViewerRenderers}
          documents={[{uri: url}]}
          theme={{primary: "rgb(92,48,125)", secondary: "#000000", tertiary: "rgba(92,48,125,0.55)", text_primary: "#ffffff", text_secondary: "#ffffff", text_tertiary: "#999999", disableThemeScrollbar: false}}
          config={{header: {disableHeader: true, disableFileName: true, retainURLParams: false}}}
        />
      );
    } else if (this.isExtForFileViewer(ext)) {
      return (
        <a target="_blank" rel="noreferrer" href={url}>
          <FileViewer key={path} fileType={ext} filePath={url} errorComponent={<div>error</div>} onError={(error) => Setting.showMessage("error", error)} />
        </a>
      );
    } else if (this.isExtForMarkdownViewer(ext)) {
      return (
        <div className="prose prose-invert max-w-none overflow-auto p-4" style={{height: this.getEditorHeightCss()}}>
          <ReactMarkdown key={path} remarkPlugins={[remarkGfm, remarkFrontmatter]}>{this.state.text}</ReactMarkdown>
        </div>
      );
    } else {
      if (this.state.loading) {
        return (
          <div className="flex justify-center pt-20">
            <Loader2 className="w-8 h-8 text-zinc-500 animate-spin" />
          </div>
        );
      }
      return (
        <div style={{height: this.getEditorHeightCss()}}>
          <Editor key={path} value={this.state.text} fillHeight />
        </div>
      );
    }
  }

  getPropertyValue(file, propertyName) {
    if (!this.props.store.propertiesMap) {return "";}
    const properties = this.props.store.propertiesMap[file.key];
    if (properties === undefined) {return "";}
    return properties[propertyName];
  }

  setPropertyValue(file, propertyName, value) {
    const store = this.props.store;
    if (store.propertiesMap[file.key] === undefined) {
      store.propertiesMap[file.key] = {};
    }
    store.propertiesMap[file.key][propertyName] = value;
    this.updateStore(store);
  }

  renderProperties() {
    if (this.state.selectedKeys.length === 0) {return null;}
    const file = this.state.selectedFile;
    if (file === null) {return null;}

    return (
      <div ref={this.filePane} className="bg-zinc-900/50 border border-zinc-800 rounded text-sm p-3">
        <div className="grid grid-cols-2 gap-2">
          <div><span className="text-zinc-500">{i18next.t("store:File name")}:</span> <span className="text-zinc-300 ml-1">{file.title}</span></div>
          {Conf.EnableExtraPages && <div><span className="text-zinc-500">{i18next.t("store:File type")}:</span> <span className="text-zinc-300 ml-1">{Setting.getExtFromFile(file)}</span></div>}
          <div><span className="text-zinc-500">{i18next.t("store:File size")}:</span> <span className="text-zinc-300 ml-1">{Setting.getFriendlyFileSize(file.size)}</span></div>
          <div><span className="text-zinc-500">{i18next.t("general:Created time")}:</span> <span className="text-zinc-300 ml-1">{Setting.getFormattedDate(file.createdTime)}</span></div>
        </div>
        {Conf.EnableExtraPages && (
          <div className="grid grid-cols-2 gap-2 mt-2">
            <div>
              <span className="text-zinc-500">{i18next.t("store:Collected time")}:</span>
              <span className="text-zinc-300 ml-1">{Setting.getFormattedDate(Setting.getCollectedTime(file.title))}</span>
              <input type="datetime-local" className="ml-2 px-2 py-1 bg-zinc-800 border border-zinc-700 rounded text-xs text-white" defaultValue={this.getPropertyValue(file, "collectedTime")} onChange={(e) => this.setPropertyValue(file, "collectedTime", e.target.value)} />
            </div>
            <div>
              <span className="text-zinc-500">{i18next.t("store:Subject")}:</span>
              <select className="ml-2 px-2 py-1 bg-zinc-800 border border-zinc-700 rounded text-xs text-white" value={this.getPropertyValue(file, "subject")} onChange={(e) => this.setPropertyValue(file, "subject", e.target.value)}>
                <option value="">--</option>
                {["Math", "Chinese", "English", "Science", "Physics", "Chemistry", "Biology", "History"].map(s => (
                  <option key={s} value={s}>{i18next.t(`store:${s}`)}</option>
                ))}
              </select>
            </div>
          </div>
        )}
      </div>
    );
  }

  getEditorHeightCss() {
    let filePaneHeight = this.filePane.current?.offsetHeight;
    if (!filePaneHeight) {filePaneHeight = 0;}
    return `calc(100vh - ${filePaneHeight + 186 - (Conf.EnableExtraPages ? 0 : 50)}px)`;
  }

  render() {
    if (this.props.store.fileTree === null) {
      return (
        <div className="flex flex-col items-center justify-center py-20">
          <AlertTriangle className="w-16 h-16 text-red-500 mb-4" />
          <p className="text-zinc-400 mb-4">{this.props.store.error}</p>
          <button onClick={() => this.props.history.push(`/stores/${this.props.store.owner}/${this.props.store.name}`)} className="px-4 py-2 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors">
            Go to Store
          </button>
        </div>
      );
    }

    return (
      <div>
        {/* Hidden file inputs for upload */}
        <input ref={this.fileInputRef} type="file" multiple className="hidden" onChange={(e) => {
          const parentKey = this.fileInputRef.current.dataset.parentKey;
          const parentFile = this.findFileNodeByKey(Setting.getTreeWithParents(this.props.store.fileTree), parentKey);
          if (parentFile && e.target.files.length > 0) {
            this.uploadFiles(parentFile, e.target.files);
          }
          e.target.value = "";
        }} />
        <div className="grid grid-cols-[1fr,2fr] gap-3">
          <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-3">
            <div className="mb-3">
              <div className="relative">
                <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-zinc-500" />
                <input
                  className="w-full pl-8 pr-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500"
                  placeholder={i18next.t("store:Please input your search term")}
                  onChange={(e) => this.setState({searchValue: e.target.value, selectedKeys: [], selectedFile: null})}
                />
              </div>
            </div>
            {this.renderTree(this.props.store)}
          </div>
          <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-3">
            <div style={{height: this.getEditorHeightCss()}} className="border border-zinc-800 rounded-lg overflow-hidden">
              {this.renderFileViewer(this.props.store)}
            </div>
            {this.renderProperties()}
          </div>
        </div>
        {this.renderUploadFileModal()}
      </div>
    );
  }
}

export default withRouter(FileTree);
