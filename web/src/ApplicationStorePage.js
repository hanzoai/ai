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
import {ChevronLeft, ChevronRight, Grid3X3, Loader2, Search} from "lucide-react";
import * as TemplateBackend from "./backend/TemplateBackend";
import * as ApplicationBackend from "./backend/ApplicationBackend";
import i18next from "i18next";
import * as Setting from "./Setting";
import moment from "moment";

class ApplicationStorePage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      templates: [],
      loading: false,
      searchText: "",
      pagination: {
        current: 1,
        pageSize: 12,
        total: 0,
      },
    };
  }

  componentDidMount() {
    this.fetchTemplates();
  }

  fetchTemplates = () => {
    this.setState({loading: true});
    const {pagination, searchText} = this.state;

    TemplateBackend.getTemplates(
      this.props.account?.name || "admin",
      pagination.current,
      pagination.pageSize,
      "name",
      searchText,
      "createdTime",
      "desc"
    )
      .then((res) => {
        this.setState({loading: false});
        if (res.status === "ok") {
          const templates = res.data || [];
          this.setState({
            templates: templates,
            pagination: {
              ...pagination,
              total: res.data2 || templates.length,
            },
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      })
      .catch((error) => {
        this.setState({loading: false});
        Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${error}`);
      });
  };

  handleSearch = (value) => {
    this.setState({
      searchText: value,
      pagination: {...this.state.pagination, current: 1},
    }, () => {
      this.fetchTemplates();
    });
  };

  handlePaginationChange = (page) => {
    this.setState({
      pagination: {
        ...this.state.pagination,
        current: page,
      },
    }, () => {
      this.fetchTemplates();
    });
  };

  handleAddApplication = (template) => {
    const randomName = Setting.getRandomName();
    const newApplication = {
      owner: this.props.account.name,
      name: `application_${randomName}`,
      createdTime: moment().format(),
      displayName: `${template.displayName} - ${randomName}`,
      description: template.description,
      template: template.name,
      namespace: `hanzo-cloud-app-${randomName}`,
      parameters: "",
      status: "Not Deployed",
    };

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
  };

  render() {
    const {templates, loading, pagination} = this.state;
    const totalPages = Math.ceil(pagination.total / pagination.pageSize);

    return (
      <div className="min-h-[calc(100vh-135px)]">
        {/* Header */}
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold text-foreground flex items-center gap-2">
            <Grid3X3 className="w-5 h-5" />
            {i18next.t("general:Application Store")}
          </h2>
          <div className="relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
            <input
              className="h-9 w-[300px] rounded-md border border-border bg-background pl-9 pr-3 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
              placeholder={i18next.t("general:Search")}
              value={this.state.searchText}
              onChange={(e) => this.setState({searchText: e.target.value})}
              onKeyDown={(e) => e.key === "Enter" && this.handleSearch(this.state.searchText)}
            />
          </div>
        </div>

        {/* Content */}
        <div className="rounded-lg border border-border bg-card p-6">
          {loading ? (
            <div className="flex items-center justify-center py-20">
              <Loader2 className="w-8 h-8 animate-spin text-muted-foreground" />
            </div>
          ) : templates.length === 0 ? (
            <div className="text-center text-muted-foreground py-20">
              {i18next.t("general:No data")}
            </div>
          ) : (
            <>
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
                {templates.map((template) => (
                  <div
                    key={`${template.owner}/${template.name}`}
                    className="rounded-lg border border-border bg-background p-4 h-[200px] flex flex-col cursor-pointer hover:border-primary/50 hover:shadow-lg transition-all"
                    onClick={() => this.handleAddApplication(template)}
                  >
                    <div className="flex items-center gap-3 mb-2">
                      {template.icon ? (
                        <img src={template.icon} alt="" className="w-10 h-10 rounded-md object-cover shrink-0" />
                      ) : (
                        <div className="w-10 h-10 rounded-md bg-secondary flex items-center justify-center shrink-0">
                          <Grid3X3 className="w-5 h-5 text-muted-foreground" />
                        </div>
                      )}
                      <h3 className="text-sm font-medium text-foreground truncate flex-1">
                        {template.displayName || template.name}
                      </h3>
                    </div>

                    <p className="text-xs text-muted-foreground flex-1 line-clamp-3">
                      {template.description || ""}
                    </p>

                    <div className="flex items-center justify-between mt-auto pt-2">
                      <div>
                        {template.version && (
                          <span className="inline-block px-2 py-0.5 rounded text-xs bg-primary/10 text-primary">
                            {template.version}
                          </span>
                        )}
                      </div>
                      <button
                        onClick={(e) => {
                          e.stopPropagation();
                          this.handleAddApplication(template);
                        }}
                        className="rounded bg-primary px-3 py-1 text-xs text-primary-foreground hover:bg-primary/90 transition-colors"
                      >
                        {i18next.t("general:Add")}
                      </button>
                    </div>
                  </div>
                ))}
              </div>

              {/* Pagination */}
              {totalPages > 1 && (
                <div className="flex items-center justify-end gap-2 mt-6">
                  <button
                    onClick={() => this.handlePaginationChange(pagination.current - 1)}
                    disabled={pagination.current <= 1}
                    className="p-1.5 rounded border border-border text-muted-foreground hover:text-foreground disabled:opacity-30 transition-colors"
                  >
                    <ChevronLeft className="w-4 h-4" />
                  </button>
                  <span className="text-sm text-muted-foreground">
                    {pagination.current} / {totalPages}
                  </span>
                  <button
                    onClick={() => this.handlePaginationChange(pagination.current + 1)}
                    disabled={pagination.current >= totalPages}
                    className="p-1.5 rounded border border-border text-muted-foreground hover:text-foreground disabled:opacity-30 transition-colors"
                  >
                    <ChevronRight className="w-4 h-4" />
                  </button>
                  <span className="text-xs text-muted-foreground ml-2">
                    {i18next.t("general:{total} in total").replace("{total}", pagination.total)}
                  </span>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    );
  }
}

export default ApplicationStorePage;
