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
import BaseListPage from "./BaseListPage";
import {ThemeDefault} from "./Conf";
import * as Setting from "./Setting";
import * as MessageBackend from "./backend/MessageBackend";
import * as ProviderBackend from "./backend/ProviderBackend";
import moment from "moment";
import i18next from "i18next";
import * as Conf from "./Conf";
import VectorTooltip from "./VectorTooltip";

class MessageListPage extends BaseListPage {
  constructor(props) {
    super(props);
    this.state = {
      ...this.state,
      providers: [],
      providerMap: {},
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

  newMessage() {
    const randomName = Setting.getRandomName();
    return {
      owner: "admin",
      name: `message_${randomName}`,
      createdTime: moment().format(),
      organization: this.props.account.owner,
      user: this.props.account.name,
      chat: "",
      replyTo: "",
      author: this.props.account.name,
      text: "Hello",
      tokenCount: 0,
      textTokenCount: 0,
      price: 0.0,
      store: this.state.storeName,
    };
  }

  addMessage() {
    const newMessage = this.newMessage();
    MessageBackend.addMessage(newMessage)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({
            pathname: `/messages/${newMessage.name}`,
            state: {isNewMessage: true},
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
    return MessageBackend.deleteMessage(this.state.data[i]);
  };

  deleteMessage(record) {
    MessageBackend.deleteMessage(record)
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

  renderDownloadXlsxButton() {
    return (
      <button className="px-2 py-1 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginRight: "10px"}> {
        const data = [];
        this.state.data.filter(item => item.author !== "AI").forEach((item, i) => {
          const row = {};
          row[i18next.t("general:Chat")] = item.chat;
          row[i18next.t("general:Message")] = item.name;
          row[i18next.t("general:Created time")] = Setting.getFormattedDate(item.createdTime);
          row[i18next.t("general:User")] = item.user;
          row[i18next.t("general:Text")] = item.text;
          row[i18next.t("message:Error text")] = item.errorText;
          data.push(row);
        });

        const sheet = Setting.json2sheet(data);
        sheet["!cols"] = [
          {wch: 15},
          {wch: 15},
          {wch: 30},
          {wch: 15},
          {wch: 50},
          {wch: 50},
        ];

        Setting.saveSheetToFile(sheet, i18next.t("general:Messages"), `${i18next.t("general:Messages")}-${Setting.getFormattedDate(moment().format())}.xlsx`);
      }}>{i18next.t("general:Download")}</button>
    );
  }

  renderTable(messages) {
    let columns = [
      // {
      //   title: i18next.t("general:Owner"),
      //   dataIndex: "owner",
      //   key: "owner",
      //   width: "90px",
      //   sorter: (a, b) => a.owner.localeCompare(b.owner),
      // },
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "100px",
        sorter: (a, b) => a.name.localeCompare(b.name),
        ...this.getColumnSearchProps("name"),
        render: (text, record, index) => {
          return (
            <Link to={`/messages/${text}`}>
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("general:Created time"),
        dataIndex: "createdTime",
        key: "createdTime",
        width: "110px",
        sorter: (a, b) => a.createdTime.localeCompare(b.createdTime),
        render: (text, record, index) => {
          return Setting.getFormattedDate(text);
        },
      },
      {
        title: i18next.t("general:User"),
        dataIndex: "user",
        key: "user",
        width: "90px",
        sorter: (a, b) => a.user.localeCompare(b.user),
        ...this.getColumnSearchProps("user"),
        render: (text, record, index) => {
          if (text.startsWith("u-")) {
            return text;
          }

          return (
            <a target="_blank" rel="noreferrer" href={Setting.getMyProfileUrl(this.props.account).replace("/account", `/users/${Conf.AuthConfig.organizationName}/${text}`)}>
              {text}
            </a>
          );
        },
      },
      {
        title: i18next.t("general:Chat"),
        dataIndex: "chat",
        key: "chat",
        width: "90px",
        sorter: (a, b) => a.chat.localeCompare(b.chat),
        ...this.getColumnSearchProps("chat"),
        render: (text, record, index) => {
          return (
            <Link to={`/chats/${text}`}>
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("message:Reply to"),
        dataIndex: "replyTo",
        key: "replyTo",
        width: "90px",
        sorter: (a, b) => a.replyTo.localeCompare(b.replyTo),
        ...this.getColumnSearchProps("replyTo"),
        render: (text, record, index) => {
          return (
            <Link to={`/messages/${text}`}>
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("message:Author"),
        dataIndex: "author",
        key: "author",
        width: "90px",
        sorter: (a, b) => a.author.localeCompare(b.author),
        ...this.getColumnSearchProps("author"),
        render: (text, record, index) => {
          if (text === "AI") {
            return text;
          }

          if (text.startsWith("u-")) {
            return text;
          }

          let userId = text;
          if (!userId.includes("/")) {
            userId = `${record.organization}/${userId}`;
          }

          return (
            <a target="_blank" rel="noreferrer" href={Setting.getMyProfileUrl(this.props.account).replace("/account", `/users/${userId}`)}>
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
          if (!a.modelProvider) {
            return -1;
          }
          if (!b.modelProvider) {
            return 1;
          }
          return a.modelProvider.localeCompare(b.modelProvider);
        },
        ...this.getColumnSearchProps("modelProvider"),
        render: (text, record, index) => {
          if (!text) {
            return null;
          }
          const provider = this.state.providerMap[text];
          if (!provider) {
            return text;
          }
          return (
            <a target="_blank" rel="noreferrer" href={`/providers/${text}`}>
              <img width={36} height={36} src={Setting.getProviderLogoURL({category: provider.category, type: provider.type})} alt={provider.type} title={provider.type} />
            </a>
          );
        },
      },
      {
        title: i18next.t("chat:Token count"),
        dataIndex: "tokenCount",
        key: "tokenCount",
        width: "90px",
        sorter: (a, b) => a.tokenCount - b.tokenCount,
        // ...this.getColumnSearchProps("tokenCount"),
      },
      {
        title: i18next.t("chat:Text token count"),
        dataIndex: "textTokenCount",
        key: "textTokenCount",
        width: "100px",
        sorter: (a, b) => a.textTokenCount - b.textTokenCount,
        // ...this.getColumnSearchProps("tokenCount"),
      },
      {
        title: i18next.t("chat:Price"),
        dataIndex: "price",
        key: "price",
        width: "120px",
        sorter: (a, b) => a.price - b.price,
        // ...this.getColumnSearchProps("price"),
        render: (text, record, index) => {
          return Setting.getDisplayPrice(text, record.currency);
        },
      },
      {
        title: i18next.t("general:Reasoning text"),
        dataIndex: "reasonText",
        key: "reasonText",
        width: "300px",
        sorter: (a, b) => a.reasonText.localeCompare(b.reasonText),
        ...this.getColumnSearchProps("reasonText"),
        render: (text, record, index) => {
          return (
            <div dangerouslySetInnerHTML={{__html: text}} />
          );
        },
      },
      {
        title: i18next.t("general:Text"),
        dataIndex: "text",
        key: "text",
        width: "300px",
        sorter: (a, b) => a.text.localeCompare(b.text),
        ...this.getColumnSearchProps("text"),
        render: (text, record, index) => {
          return (
            <div dangerouslySetInnerHTML={{__html: text}} />
          );
        },
      },
      {
        title: i18next.t("message:Knowledge"),
        dataIndex: "knowledge",
        key: "knowledge",
        width: "100px",
        sorter: (a, b) => a.knowledge.localeCompare(b.knowledge),
        render: (text, record, index) => {
          return record.vectorScores?.map(vectorScore => {
            return (
              <VectorTooltip key={vectorScore.vector} vectorScore={vectorScore}>
                <a target="_blank" rel="noreferrer" href={`/vectors/${vectorScore.vector}`}>
                  <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">
                    {vectorScore.score}
                  </span>
                </a>
              </VectorTooltip>
            );
          });
        },
      },
      {
        title: i18next.t("message:Suggestions"),
        dataIndex: "suggestions",
        key: "suggestions",
        width: "400px",
        render: (text, record, index) => {
          return (
            text?.map(suggestion => {
              return (
                <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">{suggestion.text}</span>
              );
            })
          );
        },
      },
      {
        title: i18next.t("message:Error text"),
        dataIndex: "errorText",
        key: "errorText",
        width: "200px",
        sorter: (a, b) => a.errorText.localeCompare(b.errorText),
        ...this.getColumnSearchProps("errorText"),
        render: (text, record, index) => {
          return (
            <div dangerouslySetInnerHTML={{__html: text}} />
          );
        },
      },
      {
        title: i18next.t("message:Comment"),
        dataIndex: "comment",
        key: "comment",
        width: "200px",
        sorter: (a, b) => a.comment.localeCompare(b.comment),
        ...this.getColumnSearchProps("comment"),
        render: (text, record, index) => {
          return (
            <div dangerouslySetInnerHTML={{__html: text}} />
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
        render: (text, record, index) => {
          return (
            <span className="px-2 py-0.5 rounded text-xs " + (text ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{text ? "ON" : "OFF"}</span>
          );
        },
      },
      {
        title: i18next.t("general:Is alerted"),
        dataIndex: "isAlerted",
        key: "isAlerted",
        width: "120px",
        sorter: (a, b) => a.isAlerted - b.isAlerted,
        ...this.getColumnFilterProps("isAlerted"),
        render: (text, record, index) => {
          return (
            <span className="px-2 py-0.5 rounded text-xs " + (text ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{text ? "ON" : "OFF"}</span>
          );
        },
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "90px",
        fixed: "right",
        render: (text, record, index) => {
          return (
            <div>
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}> this.props.history.push(`/messages/${record.name}`)}
              >
                {i18next.t("general:Edit")}
              </button>
              this.deleteMessage(record)}
                okText={i18next.t("general:OK")}
                cancelText={i18next.t("general:Cancel")}
              >
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-red-600 text-white hover:bg-red-700 disabled:opacity-50" disabled={!Setting.isLocalAdminUser(this.props.account)} style={{marginBottom: "10px"}>
                  {i18next.t("general:Delete")}
                </button>
            </div>
          );
        },
      },
    ];

    if (!this.props.account || this.props.account.name !== "admin") {
      columns = columns.filter(column => column.key !== "name" && column.key !== "tokenCount" && column.key !== "price");

      const tokenCountIndex = columns.findIndex(column => column.key === "tokenCount");
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
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(messages || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
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
    MessageBackend.getGlobalMessages(params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder, this.state.storeName)
      .then((res) => {
        this.setState({
          loading: false,
        });
        if (res.status === "ok") {
          this.setState({
            data: res.data,
            pagination: {
              ...params.pagination,
              total: res.data2,
            },
            searchText: params.searchText,
            searchedColumn: params.searchedColumn,
          });
        } else {
          if (Setting.isResponseDenied(res)) {
            this.setState({
              isAuthorized: false,
            });
          } else {
            Setting.showMessage("error", res.msg);
          }
        }
      });
  };
}

export default MessageListPage;
