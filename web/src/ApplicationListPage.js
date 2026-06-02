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
import {Link} from "react-router-dom";
import moment from "moment";
import BaseListPage from "./BaseListPage";
import * as Setting from "./Setting";
import * as ApplicationBackend from "./backend/ApplicationBackend";
import i18next from "i18next";
import * as TemplateBackend from "./backend/TemplateBackend";

class ApplicationListPage extends BaseListPage {
  constructor(props) {
    super(props);
    this.state = {
      ...this.state,
      templates: [],
      k8sStatus: null,
      k8sError: null,
      deploying: {},
    };
  }

  componentDidMount() {
    this.getTemplates();
    this.getK8sStatus();
  }

  getTemplates() {
    TemplateBackend.getTemplates(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            templates: res.data || [],
          });
        }
      });
  }

  getK8sStatus() {
    TemplateBackend.getK8sStatus()
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            k8sStatus: res.data,
            k8sError: null,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
          this.setState({
            k8sError: res.msg,
          });
        }
      })
      .catch(error => {
        this.setState({
          k8sError: error.toString(),
        });
        Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${error}`);
      });
  }

  deployApplication(record, index) {
    this.setState(prevState => ({
      deploying: {
        ...prevState.deploying,
        [index]: true,
      },
    }));

    ApplicationBackend.deployApplication(record)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully deployed"));
          this.setState({
            data: this.state.data.map((item) =>
              item.name === record.name ? {...item, ...res.data} : item
            ),
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to deploy")}: ${res.msg}`);
        }
        this.setState(prevState => ({
          deploying: {
            ...prevState.deploying,
            [index]: false,
          },
        }));
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to deploy")}: ${error}`);
        this.setState(prevState => ({
          deploying: {
            ...prevState.deploying,
            [index]: false,
          },
        }));
      });
  }

  undeployApplication(record, index) {
    this.setState(prevState => ({
      deploying: {
        ...prevState.deploying,
        [index]: true,
      },
    }));

    ApplicationBackend.undeployApplication(record.owner, record.name)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully undeployed"));
          this.setState({
            data: this.state.data.map((item) =>
              item.name === record.name ? {...item, status: i18next.t("application:Not Deployed")} : item
            ),
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to undeploy")}: ${res.msg}`);
        }
        this.setState(prevState => ({
          deploying: {
            ...prevState.deploying,
            [index]: false,
          },
        }));
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to undeploy")}: ${error}`);
        this.setState(prevState => ({
          deploying: {
            ...prevState.deploying,
            [index]: false,
          },
        }));
      });
  }

  newApplication() {
    const randomName = Setting.getRandomName();
    const defaultParameters = "";

    return {
      owner: this.props.account.name,
      name: `application_${randomName}`,
      createdTime: moment().format(),
      displayName: `${i18next.t("application:New Application")} - ${randomName}`,
      description: "",
      template: this.state.templates[0]?.name || "",
      namespace: `hanzo-cloud-app-${randomName}`,
      parameters: defaultParameters,
      status: "Not Deployed",
    };
  }

  addApplication() {
    const newApplication = this.newApplication();
    ApplicationBackend.addApplication(newApplication)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully added"));
          this.props.history.push({
            pathname: `/applications/${newApplication.name}`,
            state: {isNewApplication: true},
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
    return ApplicationBackend.deleteApplication(this.state.data[i]);
  };

  deleteApplication(record) {
    ApplicationBackend.deleteApplication(record)
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

  renderTable(applications) {
    const columns = [
      {
        title: i18next.t("general:Name"),
        dataIndex: "name",
        key: "name",
        width: "160px",
        sorter: (a, b) => a.name.localeCompare(b.name),
        ...this.getColumnSearchProps("name"),
        render: (text, record, index) => {
          return (
            <Link to={`/applications/${text}`}>
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("general:Display name"),
        dataIndex: "displayName",
        key: "displayName",
        width: "200px",
        sorter: (a, b) => a.displayName.localeCompare(b.displayName),
        ...this.getColumnSearchProps("displayName"),
      },
      {
        title: i18next.t("general:Created time"),
        dataIndex: "createdTime",
        key: "createdTime",
        width: "160px",
        sorter: (a, b) => a.createdTime.localeCompare(b.createdTime),
        render: (text, record, index) => {
          return Setting.getFormattedDate(text);
        },
      },
      {
        title: i18next.t("general:Description"),
        dataIndex: "description",
        key: "description",
        width: "250px",
        sorter: (a, b) => a.description.localeCompare(b.description),
        ...this.getColumnSearchProps("description"),
        render: (text, record, index) => {
          return (
            
              <div style={{maxWidth: "250px"}}>
                {Setting.getShortText(text, 100)}
              </div>
            
          );
        },
      },
      {
        title: i18next.t("general:Template"),
        dataIndex: "template",
        key: "template",
        width: "150px",
        sorter: (a, b) => a.template.localeCompare(b.template),
        ...this.getColumnSearchProps("template"),
        render: (text, record, index) => {
          if (text === "") {
            return null;
          }
          return (
            <Link to={`/templates/${text}`}>
              {text}
            </Link>
          );
        },
      },
      {
        title: i18next.t("template:Basic config"),
        dataIndex: "basicConfigOptions",
        key: "basicConfigOptions",
        width: "200px",
        render: (text, record, index) => {
          return (
            text?.length > 0 ? text.map((option, i) => {
              if (option.parameter === "host") {
                return <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">{option.parameter}: <a href={"http://" + option.setting} style={{textDecoration: "underline"}}>{option.setting}</a></span>;
              } else {
                return <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">{option.parameter}: {option.setting}</span>;
              }
            }) : null
          );
        },
      },
      {
        title: i18next.t("general:Status"),
        dataIndex: "status",
        key: "status",
        width: "120px",
        sorter: (a, b) => a.status.localeCompare(b.status),
        ...this.getColumnSearchProps("status"),
        render: (text, record, index) => {
          return Setting.getApplicationStatusTag(text);
        },
      },
      {
        title: i18next.t("general:URL"),
        dataIndex: "url",
        key: "url",
        width: "140px",
        render: (text, record, index) => {
          if (!text || record.status === "Not Deployed") {
            return null;
          }
          return (
            <a target="_blank" rel="noreferrer" href={text} style={{display: "flex", alignItems: "center"}}>
              
              
                {text}
              
            </a>
          );
        },
      },
      {
        title: i18next.t("general:Namespace"),
        dataIndex: "namespace",
        key: "namespace",
        width: "150px",
        sorter: (a, b) => a.namespace.localeCompare(b.namespace),
        ...this.getColumnSearchProps("namespace"),
      },
      {
        title: i18next.t("general:Action"),
        dataIndex: "action",
        key: "action",
        width: "280px",
        fixed: (Setting.isMobile()) ? "false" : "right",
        render: (text, record, index) => {
          return (
            <div>
              <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginTop: "10px", marginBottom: "10px", marginRight: "10px"}> this.props.history.push(`/applications/${record.name}`)}>{i18next.t("general:Edit")}</button>
              {
                record.status === "Not Deployed" ? (
                  <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginBottom: "10px", marginRight: "10px"}> this.deployApplication(record, index)}>
                    {i18next.t("application:Deploy")}
                  </button>
                ) : (
                  this.undeployApplication(record, index)} okText={i18next.t("general:OK")} cancelText={i18next.t("general:Cancel")}>
                    <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-red-600 text-white hover:bg-red-700" style={{marginBottom: "10px", marginRight: "10px"}>
                      {i18next.t("application:Undeploy")}
                    </button>
                )
              }
              {
                record.status !== "Not Deployed" && (
                  <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginBottom: "10px", marginRight: "10px"}> this.props.history.push(`/applications/${record.name}/view`, {application: record})}>
                    {i18next.t("general:View")}
                  </button>
                )
              }
              this.deleteApplication(record)}
                okText={i18next.t("general:OK")}
                cancelText={i18next.t("general:Cancel")}
              >
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-red-600 text-white hover:bg-red-700" style={{marginBottom: "10px"}>{i18next.t("general:Delete")}</button>
            </div>
          );
        },
      },
    ];

    const paginationProps = {
      total: this.state.pagination.total,
      showQuickJumper: true,
      showSizeChanger: true,
      pageSizeOptions: ["10", "20", "50", "100", "1000", "10000", "100000"],
      showTotal: () => i18next.t("general:{total} in total").replace("{total}", this.state.pagination.total),
    };

    return (
      <div>
        <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(applications || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
              )}
            </div>
          )}
          loading={this.state.loading}
          onChange={this.handleTableChange}
        />
      </div>
    );
  }

  fetch = (params = {}) => {
    const field = params.searchedColumn, value = params.searchText;
    const sortField = params.sortField, sortOrder = params.sortOrder;
    this.setState({loading: true});
    ApplicationBackend.getApplications(this.props.account.name, params.pagination.current, params.pagination.pageSize, field, value, sortField, sortOrder)
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

export default ApplicationListPage;
