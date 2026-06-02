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
import {AlertTriangle, Search} from "lucide-react";
import Highlighter from "react-highlight-words";
import i18next from "i18next";
import * as Setting from "./Setting";
import * as FormBackend from "./backend/FormBackend";

class BaseListPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      storeName: this.props.match?.params.storeName || Setting.getRequestStore(this.props.account),
      data: [],
      pagination: {
        current: 1,
        pageSize: 10,
      },
      loading: false,
      searchText: "",
      searchedColumn: "",
      isAuthorized: true,
      selectedRowKeys: [],
      selectedRows: [],
      formItems: [],
    };
  }

  handleStoreChange = () => {
    this.setState({
      storeName: this.props.match?.params.storeName || Setting.getRequestStore(this.props.account),
    },
    () => {
      const {pagination} = this.state;
      this.fetch({pagination});
    });
  };

  componentDidMount() {
    window.addEventListener("storeChanged", this.handleStoreChange);
  }

  componentWillUnmount() {
    if (this.state.intervalId !== null) {
      clearInterval(this.state.intervalId);
    }
    window.removeEventListener("storeChanged", this.handleStoreChange);
  }

  UNSAFE_componentWillMount() {
    const {pagination} = this.state;
    this.fetch({pagination});
    this.getForm();
  }

  getForm() {
    const tag = this.props.account.tag;
    const formType = this.props.match?.path?.replace(/^\//, "");
    let formName = formType;
    if (tag !== "") {
      formName = formType + "-tag-" + tag;
      FormBackend.getForm(this.props.account.owner, formName)
        .then(res => {
          if (res.status === "ok" && res.data) {
            this.setState({formItems: res.data.formItems});
          } else {
            this.fetchFormWithoutTag(formType);
          }
        });
    } else {
      this.fetchFormWithoutTag(formType);
    }
  }

  fetchFormWithoutTag(formName) {
    FormBackend.getForm(this.props.account.owner, formName)
      .then(res => {
        if (res.status === "ok" && res.data) {
          this.setState({formItems: res.data.formItems});
        } else {
          this.setState({formItems: []});
        }
      });
  }

  getColumnSearchProps = dataIndex => ({
    filterDropdown: ({setSelectedKeys, selectedKeys, confirm, clearFilters}) => (
      <div className="p-3 bg-zinc-900 border border-zinc-700 rounded-lg shadow-xl">
        <input
          ref={node => {
            this.searchInput = node;
          }}
          placeholder={i18next.t("general:Please input your search")}
          value={selectedKeys[0]}
          onChange={e => setSelectedKeys(e.target.value ? [e.target.value] : [])}
          onKeyDown={e => {
            if (e.key === "Enter") {this.handleSearch(selectedKeys, confirm, dataIndex);}
          }}
          className="w-full px-3 py-1.5 mb-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500"
        />
        <div className="flex gap-2">
          <button
            onClick={() => this.handleSearch(selectedKeys, confirm, dataIndex)}
            className="flex items-center gap-1 px-3 py-1 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors"
          >
            <Search className="w-3 h-3" />
            {i18next.t("general:Search")}
          </button>
          <button
            onClick={() => this.handleReset(clearFilters)}
            className="px-3 py-1 bg-zinc-800 text-zinc-300 rounded text-xs hover:bg-zinc-700 transition-colors"
          >
            {i18next.t("general:Reset")}
          </button>
          <button
            onClick={() => {
              confirm({closeDropdown: false});
              this.setState({
                searchText: selectedKeys[0],
                searchedColumn: dataIndex,
              });
            }}
            className="px-3 py-1 text-zinc-400 text-xs hover:text-white transition-colors"
          >
            {i18next.t("general:Filter")}
          </button>
        </div>
      </div>
    ),
    filterIcon: filtered => <Search className={`w-4 h-4 ${filtered ? "text-blue-400" : "text-zinc-500"}`} />,
    onFilter: (value, record) =>
      record[dataIndex]
        ? record[dataIndex].toString().toLowerCase().includes(value.toLowerCase())
        : "",
    onFilterDropdownOpenChange: visible => {
      if (visible) {
        setTimeout(() => this.searchInput?.select(), 100);
      }
    },
    render: text =>
      this.state.searchedColumn === dataIndex ? (
        <Highlighter
          highlightStyle={{backgroundColor: "#fbbf24", padding: 0, color: "black"}}
          searchWords={[this.state.searchText]}
          autoEscape
          textToHighlight={text ? text.toString() : ""}
        />
      ) : (
        text
      ),
  });

  getColumnFilterProps = dataIndex => ({
    filterMultiple: false,
    filters: [
      {text: i18next.t("general:ON"), value: true},
      {text: i18next.t("general:OFF"), value: false},
    ],
    onFilter: (value, record) => record[dataIndex] === value,
  });

  getRowSelection = () => ({
    selectedRowKeys: this.state.selectedRowKeys,
    onChange: this.onSelectChange,
    onSelectAll: this.onSelectAll,
  });

  onSelectChange = (selectedRowKeys, selectedRows) => {
    this.setState({
      selectedRowKeys,
      selectedRows,
    });
  };

  onSelectAll = (selected, selectedRows) => {
    const keys = selectedRows.map(row => this.getRowKey(row));
    this.setState({
      selectedRowKeys: keys,
      selectedRows: selectedRows,
    });
  };

  getRowKey = (record) => {
    return record.key || record.id || record.name;
  };

  clearSelection = () => {
    this.setState({
      selectedRowKeys: [],
      selectedRows: [],
    });
  };

  handleBulkDelete = () => {
    const {selectedRowKeys} = this.state;
    if (!window.confirm(`${i18next.t("general:Sure to delete")}: ${selectedRowKeys.length} ${i18next.t("general:items")} ?`)) {
      return;
    }
    this.performBulkDelete(this.state.selectedRows, selectedRowKeys);
  };

  performBulkDelete = async(selectedRows, selectedRowKeys) => {
    try {
      this.setState({loading: true});

      const sortedSelectedRows = [...selectedRows].sort((a, b) => {
        const indexA = this.state.data.findIndex(item => this.getRowKey(item) === this.getRowKey(a));
        const indexB = this.state.data.findIndex(item => this.getRowKey(item) === this.getRowKey(b));
        return indexB - indexA;
      });

      const deletePromises = sortedSelectedRows.map(selectedRow => {
        const index = this.state.data.findIndex(item => this.getRowKey(item) === this.getRowKey(selectedRow));
        return this.deleteItem(index);
      });

      const results = await Promise.allSettled(deletePromises);

      const failureCount = results.filter(result =>
        result.status === "rejected" || result.value.status !== "ok"
      ).length;

      if (failureCount > 0) {
        Setting.showMessage("error", `${failureCount} ${i18next.t("general:Failed to delete")}`);
      }

      this.clearSelection();

      const {pagination} = this.state;
      this.fetch({pagination});
    } catch (error) {
      Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
    } finally {
      this.setState({loading: false});
    }
  };

  deleteItem = async(item) => {
    throw new Error("deleteItem method must be implemented by subclass");
  };

  handleSearch = (selectedKeys, confirm, dataIndex) => {
    this.fetch({searchText: selectedKeys[0], searchedColumn: dataIndex, pagination: this.state.pagination});
  };

  handleReset = clearFilters => {
    clearFilters();
    const {pagination} = this.state;
    this.fetch({pagination});
  };

  handleTableChange = (pagination, filters, sorter) => {
    this.fetch({
      sortField: sorter.field,
      sortOrder: sorter.order,
      pagination,
      ...filters,
      searchText: this.state.searchText,
      searchedColumn: this.state.searchedColumn,
    });
  };

  render() {
    if (!this.state.isAuthorized) {
      return (
        <div className="flex flex-col items-center justify-center py-20">
          <AlertTriangle className="w-16 h-16 text-red-500 mb-4" />
          <h1 className="text-2xl font-bold text-white mb-2">403 Unauthorized</h1>
          <p className="text-zinc-400 mb-6">
            {i18next.t("general:Sorry, you do not have permission to access this page or logged in status invalid.")}
          </p>
          <a
            href="/"
            className="px-4 py-2 bg-white text-black rounded-md text-sm font-medium hover:bg-zinc-200 transition-colors"
          >
            {i18next.t("general:Back Home")}
          </a>
        </div>
      );
    }

    return (
      <div>
        {this.renderTable(this.state.data)}
      </div>
    );
  }
}

export default BaseListPage;
