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
import {Link} from "react-router-dom";
import {Table} from "antd"; // eslint-disable-line unused-imports/no-unused-imports
import moment from "moment";
import BaseListPage from "./BaseListPage";
import * as Setting from "./Setting";
import * as ChatBackend from "./backend/ChatBackend";
import * as ProviderBackend from "./backend/ProviderBackend";
import i18next from "i18next";
import * as Conf from "./Conf";
import * as MessageBackend from "./backend/MessageBackend";
import ChatBox from "./ChatBox";
import {renderText} from "./ChatMessageRender";
import {Trash2} from "lucide-react";

class ChatListPage extends BaseListPage {
  constructor(props) {
    super(props);
    this.state = {
      ...this.state,
      messagesMap: {},
      providers: [],
      providerMap: {},
      filterSingleChat: Setting.getBoolValue("filterSingleChat", false),
      maximizeMessages: this.getMaximizeMessagesFromStorage(),
    };
  }

  componentDidMount() {
    super.componentDidMount();
    this.getProviders();
  }

  getProviders() {
    ProviderBackend.getProviders("admin")
      .then((res) => {
        if (res.status === "ok") {
          const providerMap = {};
          res.data.forEach(provider => {
            providerMap[provider.name] = provider;
          });
          this.setState({
            providers: res.data,
            providerMap: providerMap,
          });
        }
      });
  }

  getMaximizeMessagesFromStorage() {
    const saved = localStorage.getItem("maximizeMessages");
    if (saved === null || saved === undefined) {
      return false;
    }
    return JSON.parse(saved) === true;
  }

  toggleMaximizeMessages = () => {
    const newValue = !this.state.maximizeMessages;
    this.setState({maximizeMessages: newValue});
    localStorage.setItem("maximizeMessages", JSON.stringify(newValue));
  };

  getMessages(chatName) {
    MessageBackend.getChatMessages("admin", chatName)
      .then((res) => {
        const messagesMap = this.state.messagesMap;
        res.data.map((message) => {
          message.html = renderText(message.text);
        });
        messagesMap[chatName] = res.data;
        this.setState({messagesMap: messagesMap});
      });
  }

  newChat() {
    const randomName = Setting.getRandomName();
    return {
      owner: "admin",
      name: `chat_${randomName}`,
      createdTime: moment().format(),
      updatedTime: moment().format(),
      organization: this.props.account.owner,
      displayName: `${i18next.t("chat:New Chat")} - ${randomName}`,
      category: i18next.t("chat:Default Category"),
      type: "AI",
      user: this.props.account.name,
      user1: "",
      user2: "",
      users: [],
      clientIp: "",
      userAgent: "",
      messageCount: 0,
      tokenCount: 0,
      needTitle: true,
      store: this.state.storeName,
    };
  }

  addChat() {
    const newChat = this.newChat();
    ChatBackend.addChat(newChat)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({
            pathname: `/chats/${newChat.name}`,
            state: {isNewChat: true},
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
      });
  }

  deleteItem = async(i) => {
    return ChatBackend.deleteChat(this.state.data[i]);
  };

  deleteChat(record) {
    ChatBackend.deleteChat(record)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deleted"));
          this.setState({
            data: this.state.data.filter((item) => item.name !== record.name),
            pagination: {
              ...this.state.pagination,
              total: this.state.pagination.total - 1,
            },
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${error}`);
      });
  }

  renderUserAgent(record) {
    if (record.userAgentDesc === "") {
      return record.userAgent;
    } else {
      return record.userAgentDesc?.split("|").map(text => {
        if (text.includes("Other") || text.includes("Generic Smartphone")) {
          return null;
        }
        return <div key={text}>{text}</div>;
      });
    }
  }

  getMessagesColumnSearchProps = () => ({
    ...this.getColumnSearchProps("messages"),
    onFilter: (value, record) => {
      const messages = this.state.messagesMap[record.name];
      if (!messages || messages.length === 0) {
        return false;
      }
      return messages.some(message =>
        message.text && message.text.toLowerCase().includes(value.toLowerCase())
      );
    },
  });

  renderTable(chats) {
    let columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "100px",
        sorter: (a, b) => a.name.localeCompare(b.name),
        ...this.getColumnSearchProps("name"),
        render: (text) => <Link to={`chats/${text}`} className="text-primary hover:underline">{text}</Link>,
      },
      {
        title: i18next.t("general:Updated time"),
        dataIndex: "updatedTime",
        key: "updatedTime",
        width: "130px",
        sorter: (a, b) => a.updatedTime.localeCompare(b.updatedTime),
        render: (text) => Setting.getFormattedDate(text),
      },
      {
        title: i18next.t("general:User"),
        dataIndex: "user",
        key: "user",
        width: "90px",
        sorter: (a, b) => a.user.localeCompare(b.user),
        ...this.getColumnSearchProps("user"),
        render: (text) => {
          if (text.startsWith("u-")) {return text;}
          return (
            <a target="_blank" rel="noreferrer" href={Setting.getMyProfileUrl(this.props.account).replace("/account", `/users/${Conf.AuthConfig.organizationName}/${text}`)} className="text-primary hover:underline">
              {text}
            </a>
          );
        },
      },
      {
        title: i18next.t("general:Model"),
        dataIndex: "modelProvider",
        key: "modelProvider",
        width: "150px",
        align: "center",
        sorter: (a, b) => {
          if (!a.modelProvider) {return -1;}
          if (!b.modelProvider) {return 1;}
          return a.modelProvider.localeCompare(b.modelProvider);
        },
        ...this.getColumnSearchProps("modelProvider"),
        render: (text) => {
          if (!text) {return null;}
          const provider = this.state.providerMap[text];
          if (!provider) {return text;}
          return (
            <a target="_blank" rel="noreferrer" href={`/providers/${text}`}>
              <img width={36} height={36} src={Setting.getProviderLogoURL({category: provider.category, type: provider.type})} alt={provider.type} title={provider.type} />
            </a>
          );
        },
      },
      {
        title: i18next.t("general:Client IP"),
        dataIndex: "clientIp",
        key: "clientIp",
        width: "120px",
        sorter: (a, b) => a.clientIp.localeCompare(b.clientIp),
        ...this.getColumnSearchProps("clientIp"),
        render: (text, record) => {
          if (text === "") {return null;}
          return (
            <a target="_blank" rel="noreferrer" href={`https://db-ip.com/${text}`} className="text-primary hover:underline">
              {record.clientIpDesc === "" ? text : (
                <div>
                  {text}<br />{record.clientIpDesc}<br /><br />{this.renderUserAgent(record)}
                </div>
              )}
            </a>
          );
        },
      },
      {
        title: i18next.t("general:Count"),
        dataIndex: "messageCount",
        key: "messageCount",
        width: "80px",
        sorter: (a, b) => a.messageCount - b.messageCount,
      },
      {
        title: i18next.t("chat:Token count"),
        dataIndex: "tokenCount",
        key: "tokenCount",
        width: "120px",
        sorter: (a, b) => a.tokenCount - b.tokenCount,
      },
      {
        title: i18next.t("chat:Price"),
        dataIndex: "price",
        key: "price",
        width: "120px",
        sorter: (a, b) => a.price - b.price,
        render: (text, record) => Setting.getDisplayPrice(text, record.currency),
      },
      {
        title: i18next.t("general:Messages"),
        dataIndex: "messages",
        key: "messages",
        width: this.state.maximizeMessages ? "70vw" : "800px",
        ...this.getMessagesColumnSearchProps(),
        render: (text, record) => {
          const messages = this.state.messagesMap[record.name];
          if (messages === undefined || messages.length === 0) {return null;}
          const messagesWidth = this.state.maximizeMessages ? "70vw" : "800px";

          return (
            <div className="p-1 m-1 bg-zinc-700 rounded-lg" style={{width: messagesWidth}}>
              <div className="max-h-[500px] overflow-y-auto w-full">
                <ChatBox disableInput={true} hideInput={true} messages={messages} sendMessage={null} account={this.props.account} previewMode={true} />
              </div>
            </div>
          );
        },
      },
      {
        title: i18next.t("general:Is deleted"),
        dataIndex: "isDeleted",
        key: "isDeleted",
        width: "120px",
        sorter: (a, b) => a.isDeleted - b.isDeleted,
        ...this.getColumnFilterProps("isDeleted"),
        render: (text) => (
          <span className={`inline-block px-2 py-0.5 rounded text-xs ${text ? "bg-green-500/20 text-green-400" : "bg-zinc-700 text-zinc-400"}`}>
            {text ? i18next.t("general:ON") : i18next.t("general:OFF")}
          </span>
        ),
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "110px",
        fixed: "right",
        render: (text, record) => (
          <div className="flex flex-col gap-2">
            <button onClick={() => this.props.history.push(`/chats/${record.name}`)} className="rounded bg-primary px-3 py-1 text-xs text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Edit")}</button>
            <button disabled={!Setting.isLocalAdminUser(this.props.account)} onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${record.name} ?`)) { this.deleteChat(record); } }} className="rounded bg-destructive px-3 py-1 text-xs text-destructive-foreground hover:bg-destructive/90 disabled:opacity-50 transition-colors">{i18next.t("general:Delete")}</button>
          </div>
        ),
      },
    ];

    if (this.state.filterSingleChat) {
      chats = chats?.filter(chat => chat.messageCount > 1);
    }

    if (!this.props.account || this.props.account.name !== "admin") {
      columns = columns.filter(column => column.key !== "name" && column.key !== "tokenCount" && column.key !== "price" && column.key !== "clientIp");

      const tokenCountIndex = columns.findIndex(column => column.key === "messageCount");
      if (tokenCountIndex !== -1) {
        const [tokenCountElement] = columns.splice(tokenCountIndex, 1);
        const actionIndex = columns.findIndex(column => column.key === "action");
        const insertIndex = actionIndex !== -1 ? actionIndex : columns.length;
        columns.splice(insertIndex, 0, tokenCountElement);
      }
    }

    const paginationProps = {
      total: this.state.pagination.total,
      showQuickJumper: true,
      showSizeChanger: true,
      pageSizeOptions: ["10", "20", "50", "100", "1000", "10000", "100000"],
      showTotal: () => i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total),
    };

    return (
      <div>
        <Table scroll={{x: "max-content"}} columns={columns} dataSource={chats} rowKey="name" rowSelection={this.getRowSelection()} size="middle" bordered pagination={paginationProps}
          title={() => (
            <div className="flex items-center gap-4 flex-wrap">
              <span className="text-foreground font-medium">{i18next.t("general:Chats")}</span>
              <button disabled={!Setting.isLocalAdminUser(this.props.account)} onClick={this.addChat.bind(this)} className="rounded bg-primary px-3 py-1 text-xs text-primary-foreground hover:bg-primary/90 disabled:opacity-50 transition-colors">{i18next.t("general:Add")}</button>
              {this.state.selectedRowKeys.length > 0 && (
                <button onClick={() => { if (window.confirm(`${i18next.t("general:Sure to delete")}: ${this.state.selectedRowKeys.length} ${i18next.t("general:items")} ?`)) { this.performBulkDelete(this.state.selectedRows, this.state.selectedRowKeys); } }} className="inline-flex items-center gap-1 rounded bg-destructive px-3 py-1 text-xs text-destructive-foreground hover:bg-destructive/90 transition-colors">
                  <Trash2 className="w-3 h-3" />
                  {i18next.t("general:Delete")} ({this.state.selectedRowKeys.length})
                </button>
              )}
              <label className="flex items-center gap-2 ml-4 cursor-pointer">
                <span className="text-sm text-muted-foreground">{i18next.t("chat:Maximize messages")}:</span>
                <input type="checkbox" checked={this.state.maximizeMessages} onChange={this.toggleMaximizeMessages} className="w-4 h-4 rounded accent-primary" />
              </label>
              <span className="text-sm text-muted-foreground ml-4">
                {i18next.t("general:Users")}: {Setting.getDisplayTag(Setting.uniqueFields(chats, "user"))}
              </span>
              <span className="text-sm text-muted-foreground">
                {i18next.t("general:Chats")}: {Setting.getDisplayTag(Setting.sumFields(chats, "count"))}
              </span>
              <span className="text-sm text-muted-foreground">
                {i18next.t("general:Messages")}: {Setting.getDisplayTag(Setting.sumFields(chats, "messageCount"))}
              </span>
              {(!this.props.account || this.props.account.name !== "admin") ? null : (
                <>
                  <span className="text-sm text-muted-foreground">
                    {i18next.t("general:Tokens")}: {Setting.getDisplayTag(Setting.sumFields(chats, "tokenCount"))}
                  </span>
                  <span className="text-sm text-muted-foreground">
                    {i18next.t("chat:Price")}: {Setting.getDisplayPrice(Setting.sumFields(chats, "price"))}
                  </span>
                </>
              )}
            </div>
          )}
          loading={this.state.loading}
          rowClassName={(record) => record.isDeleted ? "highlight-row" : ""}
          onChange={this.handleTableChange}
        />
      </div>
    );
  }

  fetch = (params = {}) => {
    let field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    if (params.type !== undefined && params.type !== null) {
      field = "type";
      value = params.type;
    }

    this.setState({loading: true});
    ChatBackend.getGlobalChats(params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder, this.state.storeName)
      .then((res) => {
        this.setState({loading: false});
        if (res.status === "ok") {
          let chats = res.data;
          if (this.props.account.name !== "admin") {
            chats = chats.filter(chat => chat.user !== "admin");
          }

          this.setState({
            data: res.data,
            pagination: {
              ...params.pagination,
              total: res.data2,
            },
            searchText: params.searchText,
            searchedColumn: params.searchedColumn,
          });

          chats.forEach((chat) => {
            if (chat.messageCount > 1) {
              this.getMessages(chat.name);
            }
          });
        } else {
          if (Setting.isResponseDenied(res)) {
            this.setState({isAuthorized: false});
          } else {
            Setting.showMessage("error", res.msg);
          }
        }
      });
  };
}

export default ChatListPage;
